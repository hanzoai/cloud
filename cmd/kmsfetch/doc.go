// kmsfetch reads secrets over the node's KMS socket for a container that holds
// no credential of its own. The socket is $ZIP_RUNTIME_DIR/kms.sock and the
// kernel says who is asking, so there is nothing to present.
//
//	kmsfetch [-out DIR] REF...              write each secret to DIR/<name>, in memory
//	kmsfetch -env NAME=REF... -- CMD ARG...  set each as an environment variable and exec CMD
//	kmsfetch install DIR                     copy this binary into DIR, for an image without it
//
// The exec form is how a container that reads its configuration from the
// environment gets a secret with no file, no shell and no Secret object: the
// value exists in the process that replaces this one and nowhere else.
package main
