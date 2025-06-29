package bot

/*******************************************************************************
 * This is very experimental code and probably a long way from perfect or
 * ideal.  Please provide feed back on areas that would improve performance
 *
 */

// Package dgvoice provides opus encoding and audio file playback for the
// Discordgo package.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
)

// NOTE: This API is not final and these are likely to change.

// Technically the below settings can be adjusted however that poses
// a lot of other problems that are not handled well at this time.
// These below values seem to provide the best overall performance
const (
	channels  int = 2                   // 1 for mono, 2 for stereo
	frameRate int = 48000               // audio sampling rate
	frameSize int = 960                 // uint16 size of each audio frame
	maxBytes  int = (frameSize * 2) * 2 // max size of opus data
)

var OnError = func(str string, err error) {
	prefix := "dgVoice: " + str
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", prefix, err)
	} else {
		fmt.Fprintln(os.Stderr, prefix)
	}
}

func SendPCM(v *discordgo.VoiceConnection, pcm <-chan []int16) {
	if pcm == nil {
		return
	}

	opusEncoder, err := gopus.NewEncoder(frameRate, channels, gopus.Audio)
	if err != nil {
		OnError("NewEncoder Error", err)
		return
	}

	for recv := range pcm {
		opus, err := opusEncoder.Encode(recv, frameSize, maxBytes)
		if err != nil {
			OnError("Encoding Error", err)
			return
		}
		if !v.Ready || v.OpusSend == nil {
			return
		}
		v.OpusSend <- opus
	}
}

// PlayAudioFile will play the given filename to the already connected
// Discord voice server/channel.  voice websocket and udp socket
// must already be setup before this will work.
func PlayAudioFile(v *discordgo.VoiceConnection, filename string, stop <-chan bool) {
	run := exec.Command("ffmpeg", "-i", filename, "-f", "s16le", "-ar", strconv.Itoa(frameRate), "-ac", strconv.Itoa(channels), "pipe:1")
	ffmpegout, err := run.StdoutPipe()
	if err != nil {
		OnError("StdoutPipe Error", err)
		return
	}

	ffmpegbuf := bufio.NewReaderSize(ffmpegout, 16384)

	err = run.Start()
	if err != nil {
		OnError("RunStart Error", err)
		return
	}

	defer func() {
		if run.Process != nil {
			_ = run.Process.Kill()
			_ = run.Wait()
		}
	}()

	// Create channels to manage shutdown
	send := make(chan []int16, 2)
	done := make(chan struct{})
	defer close(send)

	// Kill ffmpeg and stop sending PCM when signaled
	go func() {
		select {
		case <-stop:
			_ = run.Process.Kill()
		case <-done:
			// playback finished naturally
		}
	}()

	err = v.Speaking(true)
	if err != nil {
		OnError("Couldn't set speaking", err)
	}
	defer func() {
		err := v.Speaking(false)
		if err != nil {
			OnError("Couldn't stop speaking", err)
		}
	}()

	// Run SendPCM in a goroutine and wait for it to finish
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		SendPCM(v, send)
	}()

playback:
	for {
		audiobuf := make([]int16, frameSize*channels)
		err = binary.Read(ffmpegbuf, binary.LittleEndian, &audiobuf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			OnError("error reading from ffmpeg stdout", err)
			break
		}

		select {
		case send <- audiobuf:
		case <-stop:
			break playback
		}
	}

	// Signal SendPCM to exit and wait
	close(done)
	wg.Wait()
}
