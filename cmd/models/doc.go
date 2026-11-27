// Command models loads the model config a cloud deployment mounts, with the
// loader this module links, and exits 1 when it does not load.
//
//	go run ./cmd/models <values.yaml>...
//
// Each argument is a charts/app values file from hanzoai/universe. The config is
// found the way the pod finds it: the modelConfigPath env entry names a path,
// the volumeMount at that path names a volume, the volume names a ConfigMap and
// key, and the configMaps entry of that name in the same file holds the text
// (inline under data:, or under files: relative to the chart). That text is
// read by controllers.InitModelConfig from github.com/hanzoai/ai at the version
// go.mod pins, which is the call the ai bootstrap makes at boot. There a load
// error is a warning and the pod serves the static fallback, so a config the
// pinned loader cannot read has to be refused here, before an image exists.
package main
