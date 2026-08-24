package websearch

// testCSRFKey is the shared anti-forgery key this test binary holds. Mount refuses
// a verifier that invented its own — in production the value comes from KMS on the
// pod, and here one constant serves the whole binary, so a token minted anywhere in
// it verifies everywhere in it.
const testCSRFKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
