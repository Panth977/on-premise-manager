// Package apitest is the black-box API suite of opm-cloud: TestMain builds the real binary and runs it on a temp data dir;
// the tests talk HTTP and read the same data dir through driver, like the operators' tools do. Each file NN_*_test.go is
// the port of backend/test/NN-*.test.ts of the removed TypeScript harness; the subtest names are the TypeScript test
// names, and TestMappingNoTestLost holds them against testdata/ts-tests.txt.
package apitest
