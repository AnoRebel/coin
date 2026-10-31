// Package profile implements FATF, MiCA, and ISO 20022 reference profiles.
//
// A profile is a selectable set of required fields and a disclaimer, not a
// legal opinion and not a compliance certification. Validation returns the
// first missing or malformed field so a caller can report it directly.
package profile

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Kind identifies a reference profile.
type Kind string

const (
	FATF     Kind = "fatf"
	MiCA     Kind = "mica"
	ISO20022 Kind = "iso20022"
)

// DefaultFATFThreshold is the default Travel Rule trigger in asset base units.
// Deployers should override it per jurisdiction.
const DefaultFATFThreshold uint64 = 1_000

// ErrInvalid wraps configuration errors.
var ErrInvalid = errors.New("profile: invalid configuration")

// Profile describes one selectable reference profile.
type Profile struct {
	Kind          Kind     `json:"kind"`
	Title         string   `json:"title"`
	Disclaimer    string   `json:"disclaimer"`
	Required      []string `json:"required"`
	Optional      []string `json:"optional"`
	RetentionDays int      `json:"retention_days,omitempty"`
}

var profiles = map[Kind]Profile{
	FATF: {
		Kind:  FATF,
		Title: "FATF Travel Rule reference",
		Disclaimer: "Reference profile only. Thresholds, record-keeping, and Travel Rule " +
			"obligations vary by jurisdiction; the deployer remains responsible for legal compliance.",
		Required: []string{"originator_name", "originator_account", "beneficiary_name", "beneficiary_account"},
		Optional: []string{"originator_address", "beneficiary_address", "counterparty_vasp", "purpose"},
		// Five years, a common Travel Rule record-keeping expectation.
		RetentionDays: 1825,
	},
	MiCA: {
		Kind:  MiCA,
		Title: "MiCA crypto-asset reference",
		Disclaimer: "Reference profile only. It is not legal advice and does not make a " +
			"deployment compliant with MiCA; the deployer remains responsible for registrations, " +
			"white papers, and disclosures.",
		Required: []string{"iso24165", "iso17442", "white_paper"},
		Optional: []string{"white_paper_hash", "casp_lei", "order_book_venue"},
	},
	ISO20022: {
		Kind:  ISO20022,
		Title: "ISO 20022 payment reference",
		Disclaimer: "Field mapping is informational. ISO 20022 message construction, " +
			"validation, and scheme compliance remain the deployer's responsibility.",
		Required: []string{"uetr", "end_to_end_id", "instruction_id"},
		Optional: []string{"purpose_code", "remittance_information"},
	},
}

var (
	reDTI     = regexp.MustCompile(`^[A-Z0-9]{9,32}$`)
	reLEI     = regexp.MustCompile(`^[A-Z0-9]{20}$`)
	reUETR    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	reHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rePurpose = regexp.MustCompile(`^[A-Z]{1,4}$`)
	reHexRef  = regexp.MustCompile(`^[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]{1,512}$`)
)

// Get returns a profile definition.
func Get(kind Kind) (Profile, bool) {
	p, ok := profiles[kind]
	return p, ok
}

// All lists every profile in stable order.
func All() []Profile {
	return []Profile{profiles[FATF], profiles[MiCA], profiles[ISO20022]}
}

// ValidationError names the profile and field that failed.
type ValidationError struct {
	Profile Kind
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Profile, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Profile, e.Field, e.Message)
}

// IsValidation reports whether err is a profile validation failure.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// Config selects profiles for one network.
type Config struct {
	Network               string            `json:"network"`
	Profiles              []Kind            `json:"profiles"`
	FATFThreshold         uint64            `json:"fatf_threshold"`
	JurisdictionThreshold map[string]uint64 `json:"jurisdiction_threshold,omitempty"`
}

// New builds a config with the default FATF threshold.
func New(network string, kinds ...Kind) (Config, error) {
	c := Config{Network: network, Profiles: kinds, FATFThreshold: DefaultFATFThreshold}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks that the network is named, profiles are known and unique,
// and jurisdiction thresholds are non-zero.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Network) == "" {
		return fmt.Errorf("%w: network is required", ErrInvalid)
	}
	seen := make(map[Kind]bool, len(c.Profiles))
	for _, kind := range c.Profiles {
		if _, ok := profiles[kind]; !ok {
			return fmt.Errorf("%w: unknown profile %q", ErrInvalid, kind)
		}
		if seen[kind] {
			return fmt.Errorf("%w: duplicate profile %q", ErrInvalid, kind)
		}
		seen[kind] = true
	}
	if len(seen) == 0 {
		return fmt.Errorf("%w: at least one profile is required", ErrInvalid)
	}
	if c.FATFThreshold == 0 {
		return fmt.Errorf("%w: fatf_threshold must be positive", ErrInvalid)
	}
	for jurisdiction, threshold := range c.JurisdictionThreshold {
		if strings.TrimSpace(jurisdiction) == "" || threshold == 0 {
			return fmt.Errorf("%w: bad jurisdiction threshold %q", ErrInvalid, jurisdiction)
		}
	}
	return nil
}

// Enabled reports whether a profile is selected.
func (c Config) Enabled(kind Kind) bool {
	for _, k := range c.Profiles {
		if k == kind {
			return true
		}
	}
	return false
}

// Selected lists the enabled profile definitions.
func (c Config) Selected() []Profile {
	out := make([]Profile, 0, len(c.Profiles))
	for _, kind := range c.Profiles {
		out = append(out, profiles[kind])
	}
	return out
}

// Disclaimers returns the disclaimer for every enabled profile.
func (c Config) Disclaimers() []string {
	out := make([]string, 0, len(c.Profiles))
	for _, p := range c.Selected() {
		out = append(out, p.Disclaimer)
	}
	return out
}

// Threshold returns the FATF threshold for a jurisdiction.
func (c Config) Threshold(jurisdiction string) uint64 {
	if v, ok := c.JurisdictionThreshold[jurisdiction]; ok {
		return v
	}
	return c.FATFThreshold
}

// ValidateAsset checks asset metadata against the MiCA profile.
func (c Config) ValidateAsset(metadata map[string]string) error {
	if !c.Enabled(MiCA) {
		return nil
	}
	if err := requireFields(MiCA, metadata, profiles[MiCA].Required); err != nil {
		return err
	}
	if !reDTI.MatchString(metadata["iso24165"]) {
		return &ValidationError{MiCA, "iso24165", "must be a 9-32 character ISO 24165 DTI"}
	}
	if !reLEI.MatchString(metadata["iso17442"]) {
		return &ValidationError{MiCA, "iso17442", "must be a 20 character ISO 17442 LEI"}
	}
	paper := metadata["white_paper"]
	if len(paper) > 512 || !reHexRef.MatchString(paper) || !isHTTPURL(paper) {
		return &ValidationError{MiCA, "white_paper", "must be an http(s) reference up to 512 characters"}
	}
	if hash, ok := metadata["white_paper_hash"]; ok && !reHash.MatchString(hash) {
		return &ValidationError{MiCA, "white_paper_hash", "must be 64 lowercase hex characters"}
	}
	if lei, ok := metadata["casp_lei"]; ok && !reLEI.MatchString(lei) {
		return &ValidationError{MiCA, "casp_lei", "must be a 20 character ISO 17442 LEI"}
	}
	return nil
}

// Transfer is the data checked by the FATF profile.
type Transfer struct {
	Jurisdiction string
	Asset        string
	Amount       uint64
	Fields       map[string]string
}

// ValidateTransfer requires Travel Rule fields only when the amount exceeds
// the threshold for the transfer's jurisdiction.
func (c Config) ValidateTransfer(t Transfer) error {
	if !c.Enabled(FATF) || t.Amount <= c.Threshold(t.Jurisdiction) {
		return nil
	}
	return requireFields(FATF, t.Fields, profiles[FATF].Required)
}

// ValidatePayment checks ISO 20022 payment fields.
func (c Config) ValidatePayment(fields map[string]string) error {
	if !c.Enabled(ISO20022) {
		return nil
	}
	if err := requireFields(ISO20022, fields, profiles[ISO20022].Required); err != nil {
		return err
	}
	if !reUETR.MatchString(fields["uetr"]) {
		return &ValidationError{ISO20022, "uetr", "must be a lowercase UUIDv4 UETR"}
	}
	for _, field := range []string{"end_to_end_id", "instruction_id"} {
		if n := utf8.RuneCountInString(fields[field]); n == 0 || n > 35 {
			return &ValidationError{ISO20022, field, "must be 1-35 characters"}
		}
	}
	if code, ok := fields["purpose_code"]; ok && !rePurpose.MatchString(code) {
		return &ValidationError{ISO20022, "purpose_code", "must be 1-4 uppercase letters"}
	}
	if text, ok := fields["remittance_information"]; ok && utf8.RuneCountInString(text) > 140 {
		return &ValidationError{ISO20022, "remittance_information", "must be at most 140 characters"}
	}
	return nil
}

func requireFields(kind Kind, values map[string]string, fields []string) error {
	for _, field := range fields {
		if strings.TrimSpace(values[field]) == "" {
			return &ValidationError{kind, field, "missing required field"}
		}
	}
	for _, field := range []string{"originator_name", "beneficiary_name"} {
		if kind == FATF && utf8.RuneCountInString(values[field]) > 140 {
			return &ValidationError{kind, field, "must be at most 140 characters"}
		}
	}
	for _, field := range []string{"originator_account", "beneficiary_account"} {
		if kind == FATF && utf8.RuneCountInString(values[field]) > 35 {
			return &ValidationError{kind, field, "must be at most 35 characters"}
		}
	}
	return nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
