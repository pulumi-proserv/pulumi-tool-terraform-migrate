package cmd

import "testing"

func TestResolveSubcommands(t *testing.T) {
	for _, path := range [][]string{{"resolve", "cfn"}, {"resolve", "tf"}, {"import-id-match"}} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("%v not registered: cmd=%v err=%v", path, cmd, err)
		}
	}
}
