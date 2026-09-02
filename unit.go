package cloud

// Unit is the type with one inhabitant: the In of an op that reads nothing off
// the wire, and the Out of one that answers no body.
//
// It is the type-theory name for it. noInput/noContent/void/nothing/none all
// named a transport detail — and named it eighty-one times, once per package —
// where the meaning is simply "there is no value here".
//
// IT IS AN ALIAS, and that is load-bearing. A 204 needs BOTH halves, which is
// why the two are easy to confuse and TestAnAliasAnswersNoContent measures each:
//
//   - the DOCUMENT publishes 204-with-no-content because the Out type has no NAME
//     and so projects no schema. A named empty struct publishes an empty object
//     instead, and every generated SDK then expects a body.
//   - the WIRE sends 204 because the handler returns a NIL *Out. Returning
//     &Unit{} sends 200, alias or not.
//
// So an op that answers nothing writes `return nil, nil` and types its Out here.
//
// internal/planetest writes the bare `struct{}` instead, because it sits BELOW
// this package — cloud's own tests import it, so naming Unit there is a cycle.
// The two spell the same type, which is what makes an alias the right shape.
type Unit = struct{}
