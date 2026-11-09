package plan

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Lookup says how a CSV identifier is matched to a collector.
type Lookup int

const (
	LookupAny      Lookup = iota // agent ID first, then hostname/name
	LookupHostname               // hostname or name only
	LookupID                     // agent ID only
)

func (l Lookup) String() string {
	switch l {
	case LookupHostname:
		return "hostname"
	case LookupID:
		return "agent id"
	}
	return "agent id or hostname"
}

// Row is one CSV line: an identifier, how to look it up, and the labels to set.
type Row struct {
	Identifier string
	Lookup     Lookup
	Labels     map[string]string
}

var (
	hostnameHeaders = map[string]bool{"hostname": true, "host": true, "host_name": true, "host name": true, "name": true, "server": true, "server_name": true, "ci_name": true, "fqdn": true}
	idHeaders       = map[string]bool{"id": true, "agent_id": true, "agent id": true, "agentid": true, "collector_id": true, "collector id": true}
)

func lookupFor(header string) Lookup {
	h := strings.ToLower(strings.TrimSpace(header))
	switch {
	case hostnameHeaders[h]:
		return LookupHostname
	case idHeaders[h]:
		return LookupID
	}
	return LookupAny
}

// ReadCSV parses a two column CSV. Column 1 is a hostname or agent ID. Column 2
// holds labels as "key=value" (several may share a quoted cell, comma
// separated).
//
// If the first row is a header (its second cell has no "="), it drives the rest:
// the first header chooses the lookup (hostname, agent_id/id, or either), and
// the second header becomes the label key for cells that are a bare value, so
// "hostname,environment" with a row "web01,Production" sets environment=Production.
// Cells that already contain "=" are parsed as key=value either way. Blank
// values are skipped and reported in the returned warnings.
func ReadCSV(r io.Reader) (rows []Row, warnings []string, err error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	lookup := LookupAny
	labelKey := ""
	for line := 1; ; line++ {
		rec, rerr := cr.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, nil, fmt.Errorf("line %d: %w", line, rerr)
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		if len(rec) < 2 {
			return nil, nil, fmt.Errorf("line %d: expected 2 columns, got %d", line, len(rec))
		}
		id, cell := strings.TrimSpace(rec[0]), strings.TrimSpace(rec[1])
		if line == 1 {
			id = strings.TrimPrefix(id, "\xef\xbb\xbf") // Excel exports often start with a BOM
		}
		if line == 1 && !strings.Contains(cell, "=") {
			lookup = lookupFor(id)
			labelKey = cell
			continue // header
		}
		if id == "" {
			return nil, nil, fmt.Errorf("line %d: empty identifier", line)
		}
		if cell == "" {
			warnings = append(warnings, fmt.Sprintf("line %d: %q has no value, skipped", line, id))
			continue
		}
		var labels map[string]string
		if !strings.Contains(cell, "=") && !strings.HasPrefix(cell, "-") && labelKey != "" {
			if strings.EqualFold(labelKey, FleetKey) {
				return nil, nil, fmt.Errorf("line 1: the %q column can't set the fleet label; choose a fleet in the TUI instead", labelKey)
			}
			labels = map[string]string{labelKey: cell}
		} else if labels, err = ParseLabels(cell); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, Row{Identifier: id, Lookup: lookup, Labels: labels})
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("csv has no data rows")
	}
	return rows, warnings, nil
}
