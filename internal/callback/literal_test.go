package callback

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

func TestLiteralCallbackReturnsDataWithoutExecutingIt(t *testing.T) {
	text := "~/some )+abort+execute(nope)/\n\x1b[31m"
	command, err := Parse("v:" + base64.RawURLEncoding.EncodeToString([]byte(text)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Dispatch(context.Background(), command, Dependencies{
		LookupEnv: func(string) string { return "" }, Stdout: &out,
	}); err != nil || out.String() != text {
		t.Fatalf("output=%q err=%v", out.String(), err)
	}
}
