.PHONY: test-enforcement benchmark

# Run the integration tests assuming Tetragon is installed
test-enforcement:
	@echo "Applying Tetragon Policy..."
	@tetra tracingpolicy create policy.yaml || true
	@TETRAGON_ENABLED=true go test -v integration_test.go
	@echo "Deleting Tetragon Policy..."
	@tetra tracingpolicy delete hanzo-visor-sandbox

# Run benchmarks to measure eBPF overhead
benchmark:
	@echo "Running benchmark without policy..."
	@go test -bench=BenchmarkSyscallOverhead -benchmem integration_test.go > bench_none.txt
	@echo "Applying Tetragon Policy..."
	@tetra tracingpolicy create policy.yaml || true
	@echo "Running benchmark with policy..."
	@go test -bench=BenchmarkSyscallOverhead -benchmem integration_test.go > bench_ebpf.txt
	@tetra tracingpolicy delete hanzo-visor-sandbox
	@echo "Results:"
	@cat bench_none.txt bench_ebpf.txt
