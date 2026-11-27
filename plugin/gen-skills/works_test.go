package main

import (
	"reflect"
	"testing"
)

func TestWorksOfInvertsByName(t *testing.T) {
	got, err := worksOf([]byte(`{"engineering":["git_repos","code_ask"],"data":["git_repos"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"git_repos": {"data", "engineering"}, "code_ask": {"engineering"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnnamedListsWhatTheCatalogueLacks(t *testing.T) {
	byName := map[string][]string{"git_repos": {"engineering"}, "gone_skill": {"data"}}
	if got := unnamed(byName, map[string]bool{"git_repos": true}); !reflect.DeepEqual(got, []string{"gone_skill"}) {
		t.Fatalf("got %v", got)
	}
}

func TestEveryWorkOfTheShippedTableIsNamed(t *testing.T) {
	byName, err := worksOf(worksJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) == 0 {
		t.Fatal("works.json recommends nothing")
	}
}
