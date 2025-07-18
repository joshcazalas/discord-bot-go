package bot

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/bwmarrin/dgvoice"
	"github.com/bwmarrin/discordgo"
)

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

func StartPlaybackIfNotActive(discord *discordgo.Session, guildID, textChannelID string) {
	if GlobalQueue.IsPlaying(guildID) {
		log.Printf("Already playing in guild %s, skipping duplicate call", guildID)
		return
	}

	var next VideoInfo
	var ok bool

	if GlobalQueue.IsShuffleEnabled(textChannelID) {
		next, ok = GlobalQueue.PopRandom(textChannelID)
		if !ok {
			log.Printf("Queue for channel %s is empty, nothing to play", textChannelID)
			GlobalQueue.SetInVoiceChannel(guildID, false)
			return
		}
	} else {
		next, ok = GlobalQueue.Peek(textChannelID)
		if !ok {
			log.Printf("Queue for channel %s is empty, nothing to play", textChannelID)
			GlobalQueue.SetInVoiceChannel(guildID, false)
			return
		}
	}

	vc, ok := GlobalQueue.GetVoiceConnection(guildID)
	if !ok || vc == nil {
		ErrorChan <- GuildError{
			GuildID: guildID,
			Err:     fmt.Errorf("no voice connection found for guild %s", guildID),
		}
		return
	}

	var current VideoInfo
	if GlobalQueue.IsShuffleEnabled(textChannelID) {
		current = next
	} else {
		current, ok = GlobalQueue.Pop(textChannelID)
		if !ok {
			log.Printf("Queue for channel %s is empty, nothing to play", textChannelID)
			GlobalQueue.SetInVoiceChannel(guildID, false)
			return
		}
	}

	GlobalQueue.SetCurrentlyPlaying(guildID, current)
	GlobalQueue.SetPlaying(guildID, true)

	SendNowPlayingEmbed(discord, textChannelID, current)

	currentPath, found := GlobalQueue.GetDownloadedFile(current.Title)
	if !found {
		log.Printf("File for '%s' not found — skipping and removing from queue", current.Title)
		GlobalQueue.RemoveByTitle(textChannelID, current.Title)
		ErrorChan <- GuildError{
			GuildID: guildID,
			Err:     fmt.Errorf("next track '%s' not ready yet. File not found. Skipping to the next song in the queue... ", current.Title),
		}
		StartPlaybackIfNotActive(discord, guildID, textChannelID)
		return
	}

	log.Printf("Starting playback of file %s in guild %s", currentPath, guildID)
	GlobalQueue.SetLastActivity(guildID)

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

	stop := make(chan bool)
	GlobalQueue.Lock()
	GlobalQueue.stopChans[guildID] = stop
	GlobalQueue.Unlock()

	dgvoice.PlayAudioFile(vc, currentPath, stop)

	GlobalQueue.Lock()
	delete(GlobalQueue.stopChans, guildID)
	GlobalQueue.Unlock()
	close(stop)
	close(done)

	log.Printf("Finished playing file %s in guild %s", currentPath, guildID)

	GlobalQueue.SetPlaying(guildID, false)
	GlobalQueue.SetCurrentlyPlaying(guildID, VideoInfo{})

	if err := os.Remove(currentPath); err != nil {
		log.Printf("Failed to delete file %s: %v", currentPath, err)
	}

	next, ok = GlobalQueue.Peek(textChannelID)
	if !ok {
		log.Printf("No next track in queue for channel %s", textChannelID)
		return
	}

	log.Printf("Queuing next track: %s", next.Title)
	StartPlaybackIfNotActive(discord, guildID, textChannelID)
}
