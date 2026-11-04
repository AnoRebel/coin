package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

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

// events streams finalized blocks or transaction receipts as server-sent
// events. A reconnect resumes after Last-Event-ID (or ?from=), and a cursor
// older than retention gets a cursor_expired event with the earliest cursor
// before the replay starts.
func (s *Server) events(c *echo.Context) error {
	stream := c.QueryParamOr("stream", "blocks")
	switch stream {
	case "block", "blocks":
		stream = "blocks"
	case "transaction", "transactions":
		stream = "transactions"
	default:
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "stream must be one of: blocks, transactions")
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
