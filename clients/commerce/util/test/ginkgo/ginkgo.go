package ginkgo

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/retry"
)

func Setup(suiteName string, t *testing.T) {
	log.SetVerbose(testing.Verbose())
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

var Retry = retry.Retry

// Declarations for Ginkgo DSL
var GinkgoWriter = ginkgo.GinkgoWriter
var GinkgoParallelProcess = ginkgo.GinkgoParallelProcess
var GinkgoT = ginkgo.GinkgoT
var CurrentSpecReport = ginkgo.CurrentSpecReport
var RunSpecs = ginkgo.RunSpecs
var Fail = ginkgo.Fail
var GinkgoRecover = ginkgo.GinkgoRecover
var Describe = ginkgo.Describe
var FDescribe = ginkgo.FDescribe
var PDescribe = ginkgo.PDescribe
var XDescribe = ginkgo.XDescribe
var Context = ginkgo.Context
var FContext = ginkgo.FContext
var PContext = ginkgo.PContext
var XContext = ginkgo.XContext
var It = ginkgo.It
var FIt = ginkgo.FIt
var PIt = ginkgo.PIt
var XIt = ginkgo.XIt
var By = ginkgo.By

var BeforeSuite = ginkgo.BeforeSuite
var AfterSuite = ginkgo.AfterSuite
var SynchronizedBeforeSuite = ginkgo.SynchronizedBeforeSuite
var SynchronizedAfterSuite = ginkgo.SynchronizedAfterSuite

var Before = ginkgo.BeforeEach
var After = ginkgo.AfterEach
var JustBefore = ginkgo.JustBeforeEach

var BeforeEach = ginkgo.BeforeEach
var AfterEach = ginkgo.AfterEach
var JustBeforeEach = ginkgo.JustBeforeEach

var Skip = ginkgo.Skip

// Spec-container decorators (Ginkgo v2). Re-exported so a stateful suite can
// DECLARE its real ordering constraint (Ordered → specs run in defined order,
// enabling real BeforeAll/AfterAll) instead of silently depending on file order
// under `ginkgo --randomize-all`. ContinueOnFailure keeps the rest of an Ordered
// suite running after one spec fails (useful for report-style sequences).
var Ordered = ginkgo.Ordered
var ContinueOnFailure = ginkgo.ContinueOnFailure
var Serial = ginkgo.Serial

// Declarations for Gomega DSL
var RegisterFailHandler = gomega.RegisterFailHandler
var RegisterTestingT = gomega.RegisterTestingT
var InterceptGomegaFailures = gomega.InterceptGomegaFailures
var Ω = gomega.Ω
var Expect = gomega.Expect
var ExpectWithOffset = gomega.ExpectWithOffset
var Eventually = gomega.Eventually
var EventuallyWithOffset = gomega.EventuallyWithOffset
var Consistently = gomega.Consistently
var ConsistentlyWithOffset = gomega.ConsistentlyWithOffset
var SetDefaultEventuallyTimeout = gomega.SetDefaultEventuallyTimeout
var SetDefaultEventuallyPollingInterval = gomega.SetDefaultEventuallyPollingInterval
var SetDefaultConsistentlyDuration = gomega.SetDefaultConsistentlyDuration
var SetDefaultConsistentlyPollingInterval = gomega.SetDefaultConsistentlyPollingInterval

// Declarations for Gomega Matchers
var Equal = gomega.Equal
var BeEquivalentTo = gomega.BeEquivalentTo
var BeNil = gomega.BeNil
var BeTrue = gomega.BeTrue
var BeFalse = gomega.BeFalse
var HaveOccurred = gomega.HaveOccurred
var Succeed = gomega.Succeed
var MatchError = gomega.MatchError
var BeClosed = gomega.BeClosed
var Receive = gomega.Receive
var BeSent = gomega.BeSent
var MatchRegexp = gomega.MatchRegexp
var ContainSubstring = gomega.ContainSubstring
var HavePrefix = gomega.HavePrefix
var HaveSuffix = gomega.HaveSuffix
var MatchJSON = gomega.MatchJSON
var BeEmpty = gomega.BeEmpty
var HaveLen = gomega.HaveLen
var BeZero = gomega.BeZero
var ContainElement = gomega.ContainElement
var ConsistOf = gomega.ConsistOf
var HaveKey = gomega.HaveKey
var HaveKeyWithValue = gomega.HaveKeyWithValue
var BeNumerically = gomega.BeNumerically
var BeTemporally = gomega.BeTemporally
var BeAssignableToTypeOf = gomega.BeAssignableToTypeOf
var Panic = gomega.Panic

// Helpers for nested Expect calls
func Expect1(actual interface{}, extra ...interface{}) gomega.Assertion {
	return ExpectWithOffset(1, actual, extra...)
}

func Expect2(actual interface{}, extra ...interface{}) gomega.Assertion {
	return ExpectWithOffset(2, actual, extra...)
}

func Expect3(actual interface{}, extra ...interface{}) gomega.Assertion {
	return ExpectWithOffset(3, actual, extra...)
}

// BeforeAll / AfterAll helpers
func BeforeAll(fn func()) {
	first := true
	Before(func() {
		// Only run first time BeforeEach block is executed
		if first {
			fn()
			first = false
		}
	})
}

func AfterAll(fn func()) {
	first := true
	After(func() {
		// Only run first time AfterEach block is executed
		if first {
			fn()
			first = false
		}
	})
}
