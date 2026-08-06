# The build contract is mk/plugin.mk — one file carrying every target an app
# needs: build, test, vet, describe, clean. This names the app this package
# backs and includes it, like every sibling.
#
# Without this file the app is invisible to mk/fleet.mk, whose APPDIRS is
# `wildcard apps/*/Makefile` — so check, the gate that REGENERATES the
# document from source and fails on drift, never regenerated plugin/zen and
# could not have caught a route added, moved or removed here. zen is coresident
# (it mounts as middleware on ai's /v1 and claims no prefix of its own), so its
# subset is empty TODAY and the hole cost nothing yet; an empty subset that is
# never re-derived and one that is re-derived and still empty are the same file
# and completely different facts.
APPS := zen
include ../../mk/plugin.mk
