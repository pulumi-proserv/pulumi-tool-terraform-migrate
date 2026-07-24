package cmd

import "testing"

func TestDigestSubcommands(t *testing.T) {
	for _, path := range [][]string{{"digest", "cfn"}, {"digest", "tf"}, {"tf-digest"}} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("%v not registered: cmd=%v err=%v", path, cmd, err)
		}
	}
}
