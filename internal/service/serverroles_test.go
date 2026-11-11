package service

import (
	"slices"
	"testing"
	"time"
)

func TestWantedServers(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	first := ServerFacts{Index: 1, First: true, Ready: true, Wanted: true, Running: true, JoinedAt: at(0)}

	cases := []struct {
		name    string
		servers []ServerFacts
		want    []int
	}{
		{"the first alone", []ServerFacts{first}, []int{1}},
		{"the first is always one, even if the row says otherwise", []ServerFacts{{Index: 1, First: true, Ready: true, Running: true}}, []int{1}},
		{"two ready servers add nothing", []ServerFacts{first, {Index: 2, Ready: true, JoinedAt: at(1)}}, []int{1}},
		{"two ready and one joining add nothing", []ServerFacts{first, {Index: 2, Ready: true, JoinedAt: at(1)}, {Index: 3, JoinedAt: at(2)}}, []int{1}},
		{"three ready add the oldest joined", []ServerFacts{first,
			{Index: 3, Ready: true, JoinedAt: at(2)}, {Index: 2, Ready: true, JoinedAt: at(1)}}, []int{1, 2}},
		{"the next call adds one more", []ServerFacts{first,
			{Index: 2, Ready: true, Wanted: true, Running: true, JoinedAt: at(1)}, {Index: 3, Ready: true, JoinedAt: at(2)}}, []int{1, 2, 3}},
		{"a wanted server that isn't running yet holds the next back", []ServerFacts{first,
			{Index: 2, Ready: true, Wanted: true, JoinedAt: at(1)}, {Index: 3, Ready: true, JoinedAt: at(2)}}, []int{1, 2}},
		{"the first not running holds everything back", []ServerFacts{{Index: 1, First: true, Ready: true, Wanted: true, JoinedAt: at(0)},
			{Index: 2, Ready: true, JoinedAt: at(1)}, {Index: 3, Ready: true, JoinedAt: at(2)}}, []int{1}},
		{"a fourth isn't added", []ServerFacts{first,
			{Index: 2, Ready: true, Wanted: true, Running: true, JoinedAt: at(1)},
			{Index: 3, Ready: true, Wanted: true, Running: true, JoinedAt: at(2)},
			{Index: 4, Ready: true, JoinedAt: at(3)}}, []int{1, 2, 3}},
		{"a removed Nomad server's place is taken", []ServerFacts{first,
			{Index: 2, Ready: true, Wanted: true, Running: true, JoinedAt: at(1)},
			{Index: 4, Ready: true, JoinedAt: at(3)}, {Index: 5, Ready: true, JoinedAt: at(4)}}, []int{1, 2, 4}},
		{"only ready servers are added", []ServerFacts{first,
			{Index: 2, JoinedAt: at(1)}, {Index: 3, Ready: true, JoinedAt: at(2)}, {Index: 4, Ready: true, JoinedAt: at(3)}}, []int{1, 3}},
		{"the same join time goes by index", []ServerFacts{first,
			{Index: 5, Ready: true, JoinedAt: at(1)}, {Index: 4, Ready: true, JoinedAt: at(1)}}, []int{1, 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wantedServers(c.servers); !slices.Equal(got, c.want) {
				t.Errorf("wantedServers = %v, want %v", got, c.want)
			}
		})
	}
}
