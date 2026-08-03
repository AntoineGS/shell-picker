package callback

import "testing"

func mustParse(t *testing.T, raw string) Command {
	t.Helper()
	command, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return command
}
