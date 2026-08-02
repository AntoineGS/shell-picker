package main

import (
	"reflect"
	"testing"
)

func TestFilteredGoEnvironmentRemovesControlledKeysCaseInsensitively(t *testing.T) {
	got := filteredGoEnvironment([]string{"Path=C:\\tools", "GOFLAGS=-exec=true", "goenv=hostile", "OTHER=value"})
	want := []string{"Path=C:\\tools", "OTHER=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%q want=%q", got, want)
	}
}
