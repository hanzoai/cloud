package venue

import (
	"net/http"
	"testing"
	"time"
)

// The canonical AWS "get-vanilla" Signature Version 4 test vector (AWS docs'
// Signature V4 Test Suite). Pins signV4's canonical request / string-to-sign /
// signing-key / signature end to end — the stubs never validate a signature, so
// this vector is the correctness guarantee.
func TestSigV4_GetVanillaVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := awsCreds{
		accessKey: "AKIDEXAMPLE",
		secretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	signV4(req, c, "us-east-1", "service", nil, when)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("SigV4 mismatch:\n got: %s\nwant: %s", got, want)
	}
}
