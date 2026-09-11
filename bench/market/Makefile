# The lane's own tests. Nothing here needs a network, a model or a credential.
.PHONY: test bench dry

test:
	node --test 'test/*.test.mjs'

# Every definition, validated, with what each implies but does not state.
bench:
	node bench.mjs

# One bench end to end, at no cost, into runs-dry/.
dry:
	node run.mjs --bench=code-desk --arm=all --dry --now=2026-09-10
