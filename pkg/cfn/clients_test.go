// pkg/cfn/clients_test.go
package cfn

var (
	_ StackReader        = (*cfnStackReader)(nil)
	_ CloudControlReader = (*ccReader)(nil)
	_ Lookups            = (*awsLookups)(nil)
)
