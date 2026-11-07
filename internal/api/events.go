package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/anorebel/anorebelcoin/internal/channel"
	"github.com/anorebel/anorebelcoin/internal/node"
)

// eventCursor identifies the last event a client saw. Block streams use the
// height; transaction streams add the position within the block so two
// transactions in one block resume without duplicates.
type eventCursor struct {
	height uint64
	index  int
}

func parseEventCursor(stream, raw string) (eventCursor, error) {
	if raw == "" {
		return eventCursor{}, nil
	}
	heightPart, indexPart, hasIndex := strings.Cut(raw, "-")
	height, err := strconv.ParseUint(heightPart, 10, 64)
	if err != nil {
		return eventCursor{}, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT",
			"cursor must be a block height or <height>-<transaction-index>")
	}
	cursor := eventCursor{height: height, index: -1}
	if hasIndex {
		index, err := strconv.Atoi(indexPart)
		if stream != "transactions" || err != nil || index < 0 {
			return eventCursor{}, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT",
				"transaction cursor must be <height>-<index>")
		}
		cursor.index = index
	}
	return cursor, nil
}

func (c eventCursor) String(stream string) string {
	if stream == "transactions" && c.index >= 0 {
		return fmt.Sprintf("%d-%d", c.height, c.index)
	}
	return strconv.FormatUint(c.height, 10)
}

func writeEvent(w http.ResponseWriter, id, event string, payload any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, data)
}

// events streams finalized blocks, transaction receipts, or channel messages
// as server-sent events. A reconnect resumes after Last-Event-ID (or ?from=),
// and a cursor older than retention gets a cursor_expired event with the
// earliest cursor before the replay starts.
func (s *Server) events(c *echo.Context) error {
	stream := c.QueryParamOr("stream", "blocks")
	switch stream {
	case "block", "blocks":
		stream = "blocks"
	case "transaction", "transactions":
		stream = "transactions"
	case "message", "messages":
		if s.cfg.Messages == nil {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "message stream is not configured")
		}
		return s.messageEvents(c)
	default:
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "stream must be one of: blocks, transactions, messages")
	}

	cursorRaw := c.Request().Header.Get("Last-Event-ID")
	fromQuery := false
	if cursorRaw == "" {
		cursorRaw = c.QueryParam("from")
		fromQuery = cursorRaw != ""
	}
	cursor, err := parseEventCursor(stream, cursorRaw)
	if err != nil {
		return err
	}
	// `from` is inclusive for the historic block-height form; a header or a
	// <height>-<index> cursor is the last seen event and resumes after it.
	replay := cursorRaw != ""
	transactionCursor := strings.Contains(cursorRaw, "-")
	if fromQuery && !transactionCursor {
		if cursor.height == 0 {
			replay = false
		} else {
			cursor = eventCursor{height: cursor.height - 1, index: -1}
		}
	}

	w := c.Response()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return apiErr(http.StatusInternalServerError, "UNSUPPORTED", "streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if replay {
		s.replayEvents(w, flusher, stream, cursor, transactionCursor)
	}

	ch, cancel := s.node.Subscribe()
	defer cancel()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if event.Stream() != stream {
				continue
			}
			writeEvent(w, eventID(stream, event), eventName(stream), event)
			flusher.Flush()
		}
	}
}

// replayEvents sends retained events after the cursor. If the cursor is older
// than what is retained, the client is told the earliest cursor it can use.
func (s *Server) replayEvents(w http.ResponseWriter, flusher http.Flusher, stream string, cursor eventCursor, transactionCursor bool) {
	blocks := s.node.Blocks(1000, 0)
	if len(blocks) == 0 {
		return
	}
	oldest := blocks[len(blocks)-1].Height
	expired := cursor.height+1 < oldest
	if stream == "transactions" && transactionCursor && cursor.height < oldest {
		expired = true
	}
	if expired {
		earliest := eventCursor{height: oldest, index: 0}.String(stream)
		writeEvent(w, earliest, "cursor_expired", map[string]any{"stream": stream, "earliest": earliest})
		cursor = eventCursor{height: oldest - 1, index: -1}
	}

	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		if stream == "blocks" {
			if b.Height <= cursor.height {
				continue
			}
			writeEvent(w, strconv.FormatUint(b.Height, 10), "block", blockEvent(b))
			continue
		}
		if b.Height < cursor.height {
			continue
		}
		if b.Height == cursor.height && cursor.index < 0 {
			continue
		}
		for index, hash := range b.TxHashes {
			if b.Height == cursor.height && index <= cursor.index {
				continue
			}
			receipt, ok := s.node.Receipt(hash)
			if !ok {
				continue
			}
			event := node.Event{
				Type: "transaction", Height: b.Height, Time: b.Time,
				Hash: hash, Receipt: &receipt, Index: index,
			}
			writeEvent(w, eventID(stream, event), "transaction", event)
		}
	}
	flusher.Flush()
}

// messageEvents streams one channel. Event ids are sequence numbers, so a
// reconnect resumes with Last-Event-ID (exclusive) or ?from= (inclusive).
// Hints are best-effort, so a ticker also polls the log and closes gaps.
func (s *Server) messageEvents(c *echo.Context) error {
	channelName := c.QueryParam("channel")
	if channelName == "" {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "channel is required for the message stream")
	}
	cursorRaw := c.Request().Header.Get("Last-Event-ID")
	fromQuery := false
	if cursorRaw == "" {
		cursorRaw = c.QueryParam("from")
		fromQuery = cursorRaw != ""
	}
	// Validate the channel before writing SSE headers; an error response must
	// still be JSON.
	if _, err := s.cfg.Messages.Read(channelName, 0, 1); err != nil {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid channel")
	}
	var last uint64
	if cursorRaw != "" {
		seq, err := strconv.ParseUint(cursorRaw, 10, 64)
		if err != nil {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "message cursor must be a sequence number")
		}
		if fromQuery {
			if seq > 0 {
				last = seq - 1
			}
		} else {
			last = seq
		}
	}

	w := c.Response()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return apiErr(http.StatusInternalServerError, "UNSUPPORTED", "streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Subscribe before replay so a publish between the replay and the live
	// loop still leaves a hint.
	hints, cancel := s.cfg.Messages.Subscribe(channelName)
	defer cancel()
	flusher.Flush()
	if err := s.replayMessages(w, flusher, channelName, &last); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case _, ok := <-hints:
			if !ok {
				return nil
			}
		case <-ticker.C:
		}
		if err := s.replayMessages(w, flusher, channelName, &last); err != nil {
			return err
		}
	}
}

// replayMessages sends everything after last and advances it. A cursor that
// fell outside retention gets one cursor_expired event with the earliest
// sequence, then replay restarts from there.
func (s *Server) replayMessages(w http.ResponseWriter, flusher http.Flusher, channelName string, last *uint64) error {
	for {
		page, err := s.cfg.Messages.Read(channelName, *last, 1000)
		if errors.Is(err, channel.ErrExpired) {
			earliest := uint64(0)
			if first, readErr := s.cfg.Messages.Read(channelName, 0, 1); readErr == nil && len(first) > 0 {
				earliest = first[0].Seq
			}
			id := strconv.FormatUint(earliest, 10)
			writeEvent(w, id, "cursor_expired", map[string]any{
				"stream": "messages", "channel": channelName, "earliest": id,
			})
			if earliest > 0 {
				*last = earliest - 1
			} else {
				*last = 0
			}
			flusher.Flush()
			continue
		}
		if err != nil {
			return err
		}
		for _, message := range page {
			writeEvent(w, strconv.FormatUint(message.Seq, 10), "message", map[string]any{
				"type": "message", "message": message,
			})
			*last = message.Seq
		}
		flusher.Flush()
		return nil
	}
}

// eventName is the wire event name; the stream stays plural in queries.
func eventName(stream string) string {
	if stream == "transactions" {
		return "transaction"
	}
	return "block"
}

func eventID(stream string, event node.Event) string {
	if stream == "transactions" {
		return strconv.FormatUint(event.Height, 10) + "-" + strconv.Itoa(event.Index)
	}
	return strconv.FormatUint(event.Height, 10)
}
