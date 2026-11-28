package app

import (
	"reflect"
	"slices"
	"testing"
)

func svcWith(name string, ports ...int) Service {
	s := Service{Name: name, Image: "x"}
	for i, p := range ports {
		s.Ports = append(s.Ports, Port{Name: "p" + string(rune('a'+i)), ContainerPort: p})
	}
	return s
}

func TestClaimedNames(t *testing.T) {
	pg := App{ID: "shop--production--postgres", Services: []Service{svcWith("postgres", 5432)}}
	if got := pg.ClaimedNames(pg.Services[0]); !slices.Equal(got, []string{"postgres-postgres", "postgres"}) {
		t.Errorf("one service = %v", got)
	}
	blog := App{ID: "blog", Services: []Service{svcWith("web", 80), svcWith("db", 5432), svcWith("worker")}}
	if got := blog.ClaimedNames(blog.Services[0]); !slices.Equal(got, []string{"blog-web"}) {
		t.Errorf("two services with ports = %v", got)
	}
	if got := blog.ClaimedNames(blog.Services[2]); got != nil {
		t.Errorf("no ports = %v", got)
	}
	// One service with ports beside a worker still answers to the app's name.
	api := App{ID: "api", Services: []Service{svcWith("web", 8080), svcWith("worker")}}
	if got := api.ClaimedNames(api.Services[0]); !slices.Equal(got, []string{"api-web", "api"}) {
		t.Errorf("web beside a worker = %v", got)
	}
	// An ID from before 019 that PlaceOf refuses is its own name.
	old := App{ID: "my--old--app--x", Services: []Service{svcWith("web", 80)}}
	if got := old.ClaimedNames(old.Services[0]); !slices.Equal(got, []string{"my--old--app--x-web", "my--old--app--x"}) {
		t.Errorf("old ID = %v", got)
	}
	if HostPlace(old.ID) != "default/production" || HostPlace("shop--staging--web") != "shop/staging" {
		t.Errorf("places = %q, %q", HostPlace(old.ID), HostPlace("shop--staging--web"))
	}
}

func TestAddresses(t *testing.T) {
	pg := App{ID: "shop--production--postgres", Services: []Service{svcWith("postgres", 5432)}}
	web := App{ID: "shop--production--web", Services: []Service{svcWith("web", 8080, 9090), svcWith("worker")}}
	// A Postgres of another environment claims nothing here.
	staging := App{ID: "shop--staging--postgres", Services: []Service{svcWith("postgres", 5432)}}

	got := Addresses(pg, []App{pg, web, staging})
	want := []Address{{Service: "postgres", Ports: []int{5432}, Names: []string{"postgres-postgres", "postgres"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("postgres = %+v, want %+v", got, want)
	}

	got = Addresses(web, []App{pg, web})
	want = []Address{{Service: "web", Ports: []int{8080, 9090}, Names: []string{"web-web", "web"}, Own: "web"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("web = %+v, want %+v", got, want)
	}

	t.Run("a name two apps claim", func(t *testing.T) {
		// App "web-api" (one service "x") claims "web-api-x" and "web-api";
		// app "web" with services "api" and "db" claims "web-api" and "web-db".
		a := App{ID: "shop--production--web-api", Services: []Service{svcWith("x", 80)}}
		b := App{ID: "shop--production--web", Services: []Service{svcWith("api", 80), svcWith("db", 5432)}}
		got := Addresses(a, []App{b})
		want := []Address{{Service: "x", Ports: []int{80}, Names: []string{"web-api-x"},
			Conflicts: []NameConflict{{Name: "web-api", App: b.ID, Service: "api"}}}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("web-api = %+v, want %+v", got, want)
		}
		got = Addresses(b, []App{a})
		if len(got) != 2 || got[0].Names != nil || !reflect.DeepEqual(got[0].Conflicts, []NameConflict{{Name: "web-api", App: a.ID, Service: "x"}}) ||
			!slices.Equal(got[1].Names, []string{"web-db"}) {
			t.Errorf("web = %+v", got)
		}
	})
}
