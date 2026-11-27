// Package meet is the virtual office: it decides who may join a room and mints
// the short-lived token that lets them in.
//
//	POST /v1/meet/getToken  {roomName, _id, participantName}  ->  the token, as text
//
// The MEDIA plane is not here and never will be. Audio, video and screen share ride
// a direct browser↔LiveKit WebRTC connection (LIVEKIT_WS = wss://live.hanzo.bot);
// media is not a thing to proxy through an API binary. What moved into this binary
// is the ONE decision a server has to make about a call — may this caller join this
// room — which needs the LiveKit signing key and an answer from the process that
// owns the space rows, and needs no pod of its own to hold either.
//
// ONE KEY, and it signs the ANSWER only. It is read from the SAME keys.yaml file
// the LiveKit server itself validates against (Secret `livekit-keys`, mounted
// read-only). ONE representation of that material, so it cannot drift: a second
// copy projected into env would mint tokens that look perfect and are refused at
// the media edge, which is the silent failure this whole package is trying not to
// have.
//
// The CALLER is verified by IAM and by nothing else. meet holds no key that
// verifies a caller, so there is no second bearer authority here to disagree with
// the first: a request arrives with an attested principal or it is refused. What
// that principal MAY do is not read off the token either — it is asked of apps/team,
// which owns the membership rows (see roster).
//
// Missing or ambiguous material is a 503, LOUDLY: the reason names the file and the
// Secret in the log at boot, while the caller gets an unadorned "not configured" (an
// unauthenticated 503 is not the place to enumerate our secret plumbing).
package meet
