package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/secret"
)

// marshalTransportParams serializes the variant params matching t.Type.
func marshalTransportParams(t contracts.Transport) ([]byte, error) {
	var v any
	switch t.Type {
	case contracts.TransportVlessReality:
		v = t.VlessReality
	case contracts.TransportAmneziaWG:
		v = t.AmneziaWG
	case contracts.TransportHysteria2:
		v = t.Hysteria2
	case contracts.TransportShadowsocks2022:
		v = t.Shadowsocks2022
	default:
		return nil, fmt.Errorf("postgres: unknown transport type %q", t.Type)
	}
	return json.Marshal(v)
}

// unmarshalTransportParams restores the variant pointer for typ from raw JSON.
func unmarshalTransportParams(t *contracts.Transport, typ contracts.TransportType, raw []byte) error {
	switch typ {
	case contracts.TransportVlessReality:
		var p contracts.VlessRealityParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		t.VlessReality = &p
	case contracts.TransportAmneziaWG:
		var p contracts.AmneziaWGParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		t.AmneziaWG = &p
	case contracts.TransportHysteria2:
		var p contracts.Hysteria2Params
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		t.Hysteria2 = &p
	case contracts.TransportShadowsocks2022:
		var p contracts.Shadowsocks2022Params
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		t.Shadowsocks2022 = &p
	default:
		return fmt.Errorf("postgres: unknown transport type %q", typ)
	}
	return nil
}

// insertTransports writes a node's transports on the given querier (may be a tx).
func insertTransports(ctx context.Context, q querier, nodeID string, transports []contracts.Transport) error {
	for _, t := range transports {
		params, err := marshalTransportParams(t)
		if err != nil {
			return err
		}
		id, err := secret.UUIDv4()
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `
			INSERT INTO transports (id, node_id, tag, type, version, port, enabled, priority, params)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, nodeID, t.Tag, string(t.Type), string(t.Version), t.Port, t.Enabled, t.Priority, params)
		if err != nil {
			return fmt.Errorf("postgres: insert transport %q: %w", t.Tag, err)
		}
	}
	return nil
}

// loadTransports returns transports for a set of node ids, grouped by node id.
func loadTransports(ctx context.Context, q querier, nodeIDs []string) (map[string][]contracts.Transport, error) {
	out := map[string][]contracts.Transport{}
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT node_id, tag, type, version, port, enabled, priority, params
		FROM transports WHERE node_id = ANY($1) ORDER BY priority, tag`, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: query transports: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			nodeID, tag, typ, version string
			port, priority            int
			enabled                   bool
			params                    []byte
		)
		if err := rows.Scan(&nodeID, &tag, &typ, &version, &port, &enabled, &priority, &params); err != nil {
			return nil, fmt.Errorf("postgres: scan transport: %w", err)
		}
		t := contracts.Transport{
			Tag: tag, Type: contracts.TransportType(typ), Version: contracts.TransportVersion(version),
			Port: port, Enabled: enabled, Priority: priority,
		}
		if err := unmarshalTransportParams(&t, t.Type, params); err != nil {
			return nil, err
		}
		out[nodeID] = append(out[nodeID], t)
	}
	return out, rows.Err()
}
