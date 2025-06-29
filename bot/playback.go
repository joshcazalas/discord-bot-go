package bot

import (
	"fmt"
	"log"
	"os"
	"time"

	"sync"

	"github.com/bwmarrin/discordgo"
)

type PlaybackController struct {
	StopChan        chan bool
	NewTrack        chan struct{}
	CurrentStopChan chan bool
	sync.Mutex
}

var (
	playbackControllers     = make(map[string]*PlaybackController) // guildID => controller
	playbackControllersLock sync.Mutex
)

func (ctrl *PlaybackController) SetCurrentStopChan(ch chan bool) {
	ctrl.Lock()
	defer ctrl.Unlock()
	ctrl.CurrentStopChan = ch
}

func (ctrl *PlaybackController) GetCurrentStopChan() chan bool {
	ctrl.Lock()
	defer ctrl.Unlock()
	return ctrl.CurrentStopChan
}

func (ctrl *PlaybackController) ClearCurrentStopChan() {
	ctrl.Lock()
	defer ctrl.Unlock()
	ctrl.CurrentStopChan = nil
}

func (ctrl *PlaybackController) SendStopSignal() bool {
	ctrl.Lock()
	defer ctrl.Unlock()
	if ctrl.CurrentStopChan != nil {
		select {
		case ctrl.CurrentStopChan <- true:
			return true
		default:
		}
	}
	return false
}

func findUserVoiceChannel(discord *discordgo.Session, guildID, userID string) string {
	guild, err := discord.State.Guild(guildID)
	if err != nil {
		log.Printf("Failed to fetch guild state: %v", err)
		return ""
	}

	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			return vs.ChannelID
		}
	}

	log.Printf("User %s is not in a voice channel", userID)
	return ""
}

func JoinVoiceChannel(discord *discordgo.Session, guildID, channelID string) error {
	vc, err := discord.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("failed to join voice channel: %w", err)
	}

	GlobalQueue.SaveVoiceConnection(guildID, vc)
	GlobalQueue.SetInVoiceChannel(guildID, true)

	return nil
}

func getPlaybackController(guildID string) *PlaybackController {
	playbackControllersLock.Lock()
	defer playbackControllersLock.Unlock()

	ctrl, exists := playbackControllers[guildID]
	if !exists {
		ctrl = &PlaybackController{
			StopChan: make(chan bool, 1),
			NewTrack: make(chan struct{}, 1),
		}
		playbackControllers[guildID] = ctrl
	}
	return ctrl
}

func SignalStop(guildID string) {
	ctrl := getPlaybackController(guildID)
	if !ctrl.SendStopSignal() {
		log.Printf("No active playback stop channel or channel full for guild %s", guildID)
	} else {
		log.Printf("Sent stop signal to playback controller for guild %s", guildID)
	}
}

func SignalNewTrack(guildID string) {
	ctrl := getPlaybackController(guildID)
	select {
	case ctrl.NewTrack <- struct{}{}:
		log.Printf("Sent new track signal to playback controller for guild %s", guildID)
	default:
		// Signal already sent, no need to send again
	}
}

func startPlaybackHeartbeat(guildID string) chan struct{} {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				GlobalQueue.SetLastActivity(guildID)
			case <-done:
				return
			}
		}
	}()
	return done
}

func playbackLoop(discord *discordgo.Session, guildID, textChannelID string) {
	ctrl := getPlaybackController(guildID)

	for {
		select {
		case <-ctrl.NewTrack:
			// Received signal to start new track; continue to play
		case <-ctrl.StopChan:
			log.Printf("Stopping current playback for guild %s", guildID)
			if !ctrl.SendStopSignal() {
				// no current stop channel or channel full, just log silently
			}
			ctrl.ClearCurrentStopChan()
			GlobalQueue.SetPlaying(guildID, false)
			GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})

			// Do not exit the loop; keep listening for new signals
			continue
		}

		if GlobalQueue.IsPlaying(guildID) {
			log.Printf("Playback already active in guild %s, waiting for stop or next track signal", guildID)
			select {
			case <-ctrl.StopChan:
				log.Printf("Stopping current playback for guild %s", guildID)
				if !ctrl.SendStopSignal() {
					// no current stop channel or channel full, just log silently
				}
				ctrl.ClearCurrentStopChan()
				GlobalQueue.SetPlaying(guildID, false)
				GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})

				continue
			case <-ctrl.NewTrack:
				// New track signaled, proceed
			}
		}

		var current VideoInfo
		var ok bool
		if GlobalQueue.IsShuffleEnabled(textChannelID) {
			current, ok = GlobalQueue.PopRandom(textChannelID)
		} else {
			current, ok = GlobalQueue.Pop(textChannelID)
		}

		if !ok {
			log.Printf("Queue empty for guild %s, stopping playback loop", guildID)
			GlobalQueue.SetInVoiceChannel(guildID, false)
			GlobalQueue.SetPlaying(guildID, false)
			GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})
			ctrl.ClearCurrentStopChan()
			return
		}

		GlobalQueue.SetCurrentlyPlaying(guildID, current)
		GlobalQueue.SetPlaying(guildID, true)
		SendNowPlayingEmbed(discord, textChannelID, current)

		currentPath, found := GlobalQueue.GetDownloadedFile(current.Title)
		if !found {
			log.Printf("File for '%s' not found in downloaded files — skipping", current.Title)
			GlobalQueue.RemoveByTitle(textChannelID, current.Title)
			ErrorChan <- GuildError{
				GuildID: guildID,
				Err:     fmt.Errorf("next track '%s' not ready yet. File not found. Skipping to next song", current.Title),
			}
			continue
		}

		log.Printf("Starting playback of file %s in guild %s", currentPath, guildID)
		GlobalQueue.SetLastActivity(guildID)

		stop := make(chan bool, 1)
		ctrl.SetCurrentStopChan(stop)

		done := startPlaybackHeartbeat(guildID)
		defer close(done)

		vc, ok := GlobalQueue.GetVoiceConnection(guildID)
		if !ok || vc == nil {
			log.Printf("No valid voice connection for guild %s, stopping playback loop", guildID)
			GlobalQueue.SetPlaying(guildID, false)
			GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})
			GlobalQueue.SetInVoiceChannel(guildID, false)
			ctrl.ClearCurrentStopChan()
			return
		}

		PlayAudioFile(vc, currentPath, stop)

		log.Printf("Finished playing file %s in guild %s", currentPath, guildID)

		GlobalQueue.SetPlaying(guildID, false)
		GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})

		ctrl.ClearCurrentStopChan()

		if err := os.Remove(currentPath); err != nil {
			log.Printf("Failed to delete file %s: %v", currentPath, err)
		}

		go PurgeOrphanedAudioFiles(guildID, textChannelID)
	}
}
