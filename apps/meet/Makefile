# The build contract is mk/plugin.mk — one file carrying every target an app
# needs: build, test, vet, openapi, clean. This names the app(s) this package
# backs and includes it.
#
# Without this file the app is invisible to mk/fleet.mk, whose APPDIRS is
# `wildcard apps/*/Makefile` — so `openapi-check`, the gate that REGENERATES the
# document from source and fails on drift, never regenerated plugin/meet and
# could never have caught a route added, moved or removed here.
APPS := meet
include ../../mk/plugin.mk
