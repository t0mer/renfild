// Command renfild is the Renfild voice assistant server: it receives audio from
// satellites, identifies the speaker, transcribes the command, routes it to an
// intent handler and speaks the answer back.
package main

import "github.com/t0mer/renfild/cmd"

func main() {
	cmd.Execute()
}
