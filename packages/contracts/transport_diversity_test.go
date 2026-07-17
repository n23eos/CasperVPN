package contracts

import "testing"

func tr(typ TransportType, enabled bool) Transport {
	return Transport{Type: typ, Enabled: enabled}
}

func TestDistinctEnabledTransportTypes(t *testing.T) {
	cases := []struct {
		name string
		in   []Transport
		want int
	}{
		{"empty", nil, 0},
		{"one enabled", []Transport{tr(TransportVlessReality, true)}, 1},
		{"two enabled distinct", []Transport{tr(TransportVlessReality, true), tr(TransportHysteria2, true)}, 2},
		{"two enabled same type is NOT diversity", []Transport{tr(TransportVlessReality, true), tr(TransportVlessReality, true)}, 1},
		{"disabled do not count", []Transport{tr(TransportVlessReality, true), tr(TransportHysteria2, false)}, 1},
		{"three distinct", []Transport{tr(TransportVlessReality, true), tr(TransportHysteria2, true), tr(TransportShadowsocks2022, true)}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DistinctEnabledTransportTypes(c.in); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
