// Package pdftext reads the printed text out of a PDF, in layout order.
//
// rsc.io/pdf is pure Go — no poppler, no external binary — so it works unchanged
// in the runtime image, which has no shell. It yields each drawn text fragment
// with an X/Y position rather than lines, so Lines rebuilds the printed layout:
// fragments are grouped into rows by Y proximity (top of the page first, since
// PDF Y increases upward) and ordered within a row by X. That mirrors
// `pdftotext -layout` closely enough that a row parser reads either.
//
// Two readers share it: books, which parses a bank statement's rows, and
// knowledge, which indexes a stored file's text.
package pdftext
