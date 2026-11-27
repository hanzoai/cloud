// Package evernote normalizes an Evernote .enex export into vault.Page documents.
// It is pure (no cloud/framework/I/O). An .enex file is XML: a sequence of <note>
// elements whose <content> is ENML (an XHTML dialect). Notes import flat (Evernote
// has no note hierarchy); each note's ENML body is converted to Lexical at block
// granularity via clients/knowledge/lexical.
package evernote
