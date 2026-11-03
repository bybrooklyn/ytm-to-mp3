package tag

import "os/exec"

func lookPath(n string) (string, error) { return exec.LookPath(n) }

func genSilence(ffmpeg, dst string) error {
	return exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-t", "0.2", "-y", dst).Run()
}
