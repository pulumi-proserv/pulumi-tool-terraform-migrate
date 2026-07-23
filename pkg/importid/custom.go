package importid

// composeRoute53 composes the import ID for aws:route53/record:Record.
// TODO(task-2): real implementation
func composeRoute53(get func(Role) string, provider string) (string, error) {
	return "", nil
}

// composeScalingPolicy composes the import ID for aws:appautoscaling/policy:Policy.
// TODO(task-2): real implementation
func composeScalingPolicy(get func(Role) string, provider string) (string, error) {
	return "", nil
}

// composeScalableTarget composes the import ID for aws:appautoscaling/target:Target.
// TODO(task-2): real implementation
func composeScalableTarget(get func(Role) string, provider string) (string, error) {
	return "", nil
}

// composeEcsService composes the import ID for aws:ecs/service:Service.
// TODO(task-2): real implementation
func composeEcsService(get func(Role) string, provider string) (string, error) {
	return "", nil
}

// composeTransferServer composes the import ID for aws:transfer/server:Server.
// TODO(task-2): real implementation
func composeTransferServer(get func(Role) string, provider string) (string, error) {
	return "", nil
}

// composeLayerVersionPermission composes the import ID for
// aws:lambda/layerVersionPermission:LayerVersionPermission.
// TODO(task-2): real implementation
func composeLayerVersionPermission(get func(Role) string, provider string) (string, error) {
	return "", nil
}
