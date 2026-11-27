// Package datalog is stratified Datalog over binary relations, evaluated
// semi-naively, with a proof for every tuple it derives.
//
// A RULE is a head and a body, the body a conjunction of positive atoms, negated
// atoms and the two builtins `=` and `!=`:
//
//	ancestor(X, Z) :- parent(X, Y), ancestor(Y, Z).
//	safe(X) :- node(X), not bad(X), X != "root".
//
// A variable starts with an uppercase letter or `_`, and `_` alone matches
// anything and binds nothing. A constant is double-quoted, escaping only `\"` and
// `\\`. A predicate starts with a lowercase letter; `not` is not one. The final
// period is optional. One rule per text.
//
// EVERY TUPLE IS A PAIR. A relation holds [2]string tuples and nothing else, so
// there is one index shape and one join. A predicate the rules use with ONE
// argument is the same shape with the second argument empty: p(X) is the tuple
// (X, ""). A base relation handed to a unary predicate is projected onto its
// first argument, so "X is p" reads "something says X p something". A predicate
// has one arity across a program; a program that uses one both ways is refused.
//
// SAFE BY CONSTRUCTION. Parse refuses a rule whose head, negated atoms or builtins
// name a variable no positive atom binds, and a rule with no positive atom. A
// Rule has no other constructor, so a Program holds safe rules only.
//
// STRATIFIED BY CONSTRUCTION. Compile orders the predicate dependency graph into
// its strongly connected components (Tarjan) and refuses a negated atom whose
// predicate is in its head's own component: negation inside recursion has no
// stratified meaning, and the engine that silently evaluates it anyway answers
// something nobody wrote. Components are evaluated dependencies first, so a
// negated atom always reads a relation that is already complete.
//
// SEMI-NAIVE, TEXTBOOK. Within a component every round joins each rule once per
// body position that reads the component: that position reads the tuples new last
// round, the positions before it the relation as it stood before them, the
// positions after it the whole relation. Each derivation is therefore found in
// exactly one round, and a round that finds nothing new is the fixpoint. Every
// relation is hash-indexed on each argument, and the join starts from the delta.
//
// A PROOF FOR EVERY TUPLE. The first derivation of a tuple is kept: the rule and
// the ground positive atoms it matched. Each of those was in the model before
// the tuple was, so the proofs form a DAG whose leaves are base tuples.
//
// BOUNDED, AND SAYS SO. An evaluation stops at a ceiling on the tuples it derives
// or on the bindings it tries, and names the one that stopped it. What it had
// derived by then is sound — every tuple holds, because a stratum starts only
// once every stratum below it is complete — and incomplete, which is what Cut
// says. It never returns a short model as a whole one.
package datalog
