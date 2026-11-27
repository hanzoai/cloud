// Package runs is what pods say to each other about the runs they execute: which
// sessions have a run working, so a process that did not start one can tell a run
// that is working from one that is gone.
//
// A run lives in the memory of the process that started it, and its session is a row
// in a store every pod reads. The row says "running" for a live run and for one whose
// process went away alike. This is the other half: every process that executes runs
// answers the question over the bus (sdk/coord) with the sessions it executes,
// and a process that is gone answers nothing, so its runs are held by no one. A
// sweep, a resume and a key withdrawal ask it before they touch a run.
package runs
