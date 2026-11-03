package iso20022

import (
	"encoding/xml"
	"fmt"
)

type amountXML struct {
	Currency string `xml:"Ccy,attr"`
	Value    string `xml:",chardata"`
}

type partyXML struct {
	Name   string            `xml:"Nm"`
	Postal *postalAddressXML `xml:"PstlAdr,omitempty"`
}

type postalAddressXML struct {
	Country string `xml:"Ctry"`
}

func (p partyXML) country() string {
	if p.Postal == nil {
		return ""
	}
	return p.Postal.Country
}

func newPartyXML(name, country string) partyXML {
	p := partyXML{Name: name}
	if country != "" {
		p.Postal = &postalAddressXML{Country: country}
	}
	return p
}

type accountXML struct {
	IBAN string `xml:"Id>IBAN"`
}

type agentXML struct {
	BIC string `xml:"FinInstnId>BICFI"`
}

type groupHeaderXML struct {
	MessageID  string `xml:"MsgId"`
	CreatedAt  string `xml:"CreDtTm"`
	Count      int    `xml:"NbOfTxs"`
	ControlSum string `xml:"CtrlSum,omitempty"`
}

type remittanceXML struct {
	Unstructured string `xml:"Ustrd"`
}

type pacsDocument struct {
	XMLName xml.Name `xml:"Document"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Body    pacsBody `xml:"FIToFICstmrCdtTrf"`
}

type pacsBody struct {
	Header groupHeaderXML `xml:"GrpHdr"`
	Tx     pacsTx         `xml:"CdtTrfTxInf"`
}

type pacsTx struct {
	PaymentID struct {
		EndToEndID string `xml:"EndToEndId"`
	} `xml:"PmtId"`
	Amount        amountXML      `xml:"IntrBkSttlmAmt"`
	Debtor        partyXML       `xml:"Dbtr"`
	DebtorAccount accountXML     `xml:"DbtrAcct"`
	DebtorAgent   agentXML       `xml:"DbtrAgt"`
	Creditor      partyXML       `xml:"Cdtr"`
	CreditorAcct  accountXML     `xml:"CdtrAcct"`
	CreditorAgent agentXML       `xml:"CdtrAgt"`
	Remittance    *remittanceXML `xml:"RmtInf,omitempty"`
}

// BuildPacs008 renders a pacs.008 customer credit transfer.
func BuildPacs008(t Transfer) ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	doc := pacsDocument{Xmlns: Pacs008Namespace, Body: pacsBody{
		Header: groupHeaderXML{MessageID: t.MessageID, CreatedAt: t.CreatedAt, Count: 1},
		Tx: pacsTx{
			PaymentID: struct {
				EndToEndID string `xml:"EndToEndId"`
			}{EndToEndID: t.EndToEndID},
			Amount:        amountXML{Currency: t.Amount.Currency, Value: t.Amount.Value},
			Debtor:        newPartyXML(t.Debtor.Name, t.Debtor.Country),
			DebtorAccount: accountXML{IBAN: t.Debtor.Account},
			DebtorAgent:   agentXML{BIC: t.Debtor.Agent},
			Creditor:      newPartyXML(t.Creditor.Name, t.Creditor.Country),
			CreditorAcct:  accountXML{IBAN: t.Creditor.Account},
			CreditorAgent: agentXML{BIC: t.Creditor.Agent},
			Remittance:    remittance(t.Remittance),
		},
	}}
	return marshal(doc)
}

// ParsePacs008 accepts a pacs.008 message with exactly one transfer.
func ParsePacs008(raw []byte) (Transfer, error) {
	var doc pacsDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return Transfer{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if doc.XMLName.Local != "Document" || doc.Body.Header.MessageID == "" {
		return Transfer{}, ErrMessageType
	}
	if doc.Body.Header.Count != 1 {
		return Transfer{}, fmt.Errorf("%w: pacs.008 mapping expects one transaction", ErrInvalid)
	}
	t := Transfer{
		MessageID:  doc.Body.Header.MessageID,
		EndToEndID: doc.Body.Tx.PaymentID.EndToEndID,
		CreatedAt:  doc.Body.Header.CreatedAt,
		Amount:     Amount{Currency: doc.Body.Tx.Amount.Currency, Value: doc.Body.Tx.Amount.Value},
		Debtor: Party{
			Name: doc.Body.Tx.Debtor.Name, Country: doc.Body.Tx.Debtor.country(),
			Account: doc.Body.Tx.DebtorAccount.IBAN, Agent: doc.Body.Tx.DebtorAgent.BIC,
		},
		Creditor: Party{
			Name: doc.Body.Tx.Creditor.Name, Country: doc.Body.Tx.Creditor.country(),
			Account: doc.Body.Tx.CreditorAcct.IBAN, Agent: doc.Body.Tx.CreditorAgent.BIC,
		},
		Remittance: remittanceText(doc.Body.Tx.Remittance),
	}
	if err := t.Validate(); err != nil {
		return Transfer{}, err
	}
	return t, nil
}

type painDocument struct {
	XMLName xml.Name `xml:"Document"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Body    painBody `xml:"CstmrCdtTrfInitn"`
}

type painBody struct {
	Header  groupHeaderXML  `xml:"GrpHdr"`
	Payment painPaymentInfo `xml:"PmtInf"`
}

type painPaymentInfo struct {
	PaymentID  string     `xml:"PmtInfId"`
	Count      int        `xml:"NbOfTxs"`
	ControlSum string     `xml:"CtrlSum,omitempty"`
	Debtor     partyXML   `xml:"Dbtr"`
	DebtorAcct accountXML `xml:"DbtrAcct"`
	DebtorAgt  agentXML   `xml:"DbtrAgt"`
	Tx         painTx     `xml:"CdtTrfTxInf"`
}

type painTx struct {
	PaymentID struct {
		EndToEndID string `xml:"EndToEndId"`
	} `xml:"PmtId"`
	Amount struct {
		Instd amountXML `xml:"InstdAmt"`
	} `xml:"Amt"`
	Creditor      partyXML       `xml:"Cdtr"`
	CreditorAcct  accountXML     `xml:"CdtrAcct"`
	CreditorAgent agentXML       `xml:"CdtrAgt"`
	Remittance    *remittanceXML `xml:"RmtInf,omitempty"`
}

// BuildPain001 renders a pain.001 customer credit transfer initiation.
func BuildPain001(t Transfer) ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	doc := painDocument{Xmlns: Pain001Namespace, Body: painBody{
		Header: groupHeaderXML{MessageID: t.MessageID, CreatedAt: t.CreatedAt, Count: 1, ControlSum: t.Amount.Value},
		Payment: painPaymentInfo{
			PaymentID: t.MessageID, Count: 1, ControlSum: t.Amount.Value,
			Debtor:     newPartyXML(t.Debtor.Name, t.Debtor.Country),
			DebtorAcct: accountXML{IBAN: t.Debtor.Account},
			DebtorAgt:  agentXML{BIC: t.Debtor.Agent},
			Tx: painTx{
				PaymentID: struct {
					EndToEndID string `xml:"EndToEndId"`
				}{EndToEndID: t.EndToEndID},
				Amount: struct {
					Instd amountXML `xml:"InstdAmt"`
				}{Instd: amountXML{Currency: t.Amount.Currency, Value: t.Amount.Value}},
				Creditor:      newPartyXML(t.Creditor.Name, t.Creditor.Country),
				CreditorAcct:  accountXML{IBAN: t.Creditor.Account},
				CreditorAgent: agentXML{BIC: t.Creditor.Agent},
				Remittance:    remittance(t.Remittance),
			},
		},
	}}
	return marshal(doc)
}

// ParsePain001 accepts a pain.001 message with exactly one instruction.
func ParsePain001(raw []byte) (Transfer, error) {
	var doc painDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return Transfer{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if doc.XMLName.Local != "Document" || doc.Body.Header.MessageID == "" {
		return Transfer{}, ErrMessageType
	}
	payment := doc.Body.Payment
	if doc.Body.Header.Count != 1 || payment.Count != 1 {
		return Transfer{}, fmt.Errorf("%w: pain.001 mapping expects one transaction", ErrInvalid)
	}
	t := Transfer{
		MessageID:  doc.Body.Header.MessageID,
		EndToEndID: payment.Tx.PaymentID.EndToEndID,
		CreatedAt:  doc.Body.Header.CreatedAt,
		Amount:     Amount{Currency: payment.Tx.Amount.Instd.Currency, Value: payment.Tx.Amount.Instd.Value},
		Debtor: Party{
			Name: payment.Debtor.Name, Country: payment.Debtor.country(),
			Account: payment.DebtorAcct.IBAN, Agent: payment.DebtorAgt.BIC,
		},
		Creditor: Party{
			Name: payment.Tx.Creditor.Name, Country: payment.Tx.Creditor.country(),
			Account: payment.Tx.CreditorAcct.IBAN, Agent: payment.Tx.CreditorAgent.BIC,
		},
		Remittance: remittanceText(payment.Tx.Remittance),
	}
	if payment.ControlSum != "" && payment.ControlSum != t.Amount.Value {
		return Transfer{}, fmt.Errorf("%w: control sum does not match the amount", ErrInvalid)
	}
	if err := t.Validate(); err != nil {
		return Transfer{}, err
	}
	return t, nil
}

type camtDocument struct {
	XMLName xml.Name `xml:"Document"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Body    camtBody `xml:"BkToCstmrStmt"`
}

type camtBody struct {
	Header    groupHeaderXML `xml:"GrpHdr"`
	Statement camtStatement  `xml:"Stmt"`
}

type camtStatement struct {
	ID      string `xml:"Id"`
	Account struct {
		IBAN     string `xml:"Id>IBAN"`
		Currency string `xml:"Ccy"`
	} `xml:"Acct"`
	Balances []camtBalance `xml:"Bal"`
	Entries  []camtEntry   `xml:"Ntry"`
}

type camtBalance struct {
	Type struct {
		Code string `xml:"CdOrPrtry>Cd"`
	} `xml:"Tp"`
	Amount    amountXML `xml:"Amt"`
	Direction string    `xml:"CdtDbtInd"`
}

type camtEntry struct {
	Amount     amountXML        `xml:"Amt"`
	Direction  string           `xml:"CdtDbtInd"`
	Status     string           `xml:"Sts"`
	BookingDay string           `xml:"BookgDt>Dt"`
	Details    *entryDetailsXML `xml:"NtryDtls,omitempty"`
}

type entryDetailsXML struct {
	TxDetails *txDetailsXML `xml:"TxDtls"`
}

type txDetailsXML struct {
	Refs    *refsXML           `xml:"Refs,omitempty"`
	Parties *relatedPartiesXML `xml:"RltdPties,omitempty"`
}

type refsXML struct {
	EndToEndID string `xml:"EndToEndId"`
}

type relatedPartiesXML struct {
	Debtor   *partyNameXML `xml:"Dbtr,omitempty"`
	Creditor *partyNameXML `xml:"Cdtr,omitempty"`
}

type partyNameXML struct {
	Name string `xml:"Nm"`
}

// BuildCamt053 renders a camt.053 bank statement.
func BuildCamt053(s Statement) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	stmt := camtStatement{
		ID: s.MessageID,
		Account: struct {
			IBAN     string `xml:"Id>IBAN"`
			Currency string `xml:"Ccy"`
		}{IBAN: s.Account, Currency: s.Currency},
	}
	for _, balance := range s.Balances {
		item := camtBalance{
			Amount:    amountXML{Currency: balance.Amount.Currency, Value: balance.Amount.Value},
			Direction: balance.Direction,
		}
		item.Type.Code = balance.Type
		stmt.Balances = append(stmt.Balances, item)
	}
	for _, entry := range s.Entries {
		item := camtEntry{
			Amount:     amountXML{Currency: entry.Amount.Currency, Value: entry.Amount.Value},
			Direction:  entry.Direction,
			Status:     entry.Status,
			BookingDay: entry.BookingDay,
		}
		if details := entryDetails(entry); details != nil {
			item.Details = &entryDetailsXML{TxDetails: details}
		}
		stmt.Entries = append(stmt.Entries, item)
	}
	doc := camtDocument{Xmlns: Camt053Namespace, Body: camtBody{
		Header:    groupHeaderXML{MessageID: s.MessageID, CreatedAt: s.CreatedAt, Count: len(s.Entries)},
		Statement: stmt,
	}}
	return marshal(doc)
}

// ParseCamt053 accepts a camt.053 message with exactly one statement.
func ParseCamt053(raw []byte) (Statement, error) {
	var doc camtDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return Statement{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if doc.XMLName.Local != "Document" || doc.Body.Header.MessageID == "" || doc.Body.Statement.ID == "" {
		return Statement{}, ErrMessageType
	}
	s := Statement{
		MessageID: doc.Body.Header.MessageID,
		CreatedAt: doc.Body.Header.CreatedAt,
		Account:   doc.Body.Statement.Account.IBAN,
		Currency:  doc.Body.Statement.Account.Currency,
	}
	for _, balance := range doc.Body.Statement.Balances {
		s.Balances = append(s.Balances, Balance{
			Type:      balance.Type.Code,
			Amount:    Amount{Currency: balance.Amount.Currency, Value: balance.Amount.Value},
			Direction: balance.Direction,
		})
	}
	for _, entry := range doc.Body.Statement.Entries {
		parsed := Entry{
			Amount:     Amount{Currency: entry.Amount.Currency, Value: entry.Amount.Value},
			Direction:  entry.Direction,
			Status:     entry.Status,
			BookingDay: entry.BookingDay,
		}
		if details := entry.Details; details != nil && details.TxDetails != nil {
			if refs := details.TxDetails.Refs; refs != nil {
				parsed.EndToEndID = refs.EndToEndID
			}
			if parties := details.TxDetails.Parties; parties != nil {
				if parties.Debtor != nil {
					parsed.DebtorName = parties.Debtor.Name
				}
				if parties.Creditor != nil {
					parsed.Creditor = parties.Creditor.Name
				}
			}
		}
		s.Entries = append(s.Entries, parsed)
	}
	if err := s.Validate(); err != nil {
		return Statement{}, err
	}
	return s, nil
}

// remittance omits the block entirely when there is no free-text remittance,
// because an empty Ustrd is not valid ISO 20022.
func remittance(value string) *remittanceXML {
	if value == "" {
		return nil
	}
	return &remittanceXML{Unstructured: value}
}

func remittanceText(r *remittanceXML) string {
	if r == nil {
		return ""
	}
	return r.Unstructured
}

// entryDetails builds NtryDtls only for the optional parts that are present.
func entryDetails(entry Entry) *txDetailsXML {
	if entry.EndToEndID == "" && entry.DebtorName == "" && entry.Creditor == "" {
		return nil
	}
	details := &txDetailsXML{}
	if entry.EndToEndID != "" {
		details.Refs = &refsXML{EndToEndID: entry.EndToEndID}
	}
	if entry.DebtorName != "" || entry.Creditor != "" {
		details.Parties = &relatedPartiesXML{}
		if entry.DebtorName != "" {
			details.Parties.Debtor = &partyNameXML{Name: entry.DebtorName}
		}
		if entry.Creditor != "" {
			details.Parties.Creditor = &partyNameXML{Name: entry.Creditor}
		}
	}
	return details
}

func marshal(v any) ([]byte, error) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// CountTransactions reports how many credit transfers an ISO 20022 message
// carries. The mapper supports one per message; a batch is refused.
func CountTransactions(raw []byte) (int, error) {
	var probe struct {
		XMLName xml.Name
		Pacs    *struct {
			Count int `xml:"GrpHdr>NbOfTxs"`
		} `xml:"FIToFICstmrCdtTrf"`
		Pain *struct {
			Count int `xml:"PmtInf>NbOfTxs"`
		} `xml:"CstmrCdtTrfInitn"`
	}
	if err := xml.Unmarshal(raw, &probe); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	switch probe.XMLName.Local {
	case "Document":
		if probe.Pacs != nil {
			return probe.Pacs.Count, nil
		}
		if probe.Pain != nil {
			return probe.Pain.Count, nil
		}
	}
	return 0, ErrMessageType
}
