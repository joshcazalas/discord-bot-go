package bot

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func HandleSkipCommand(discord *discordgo.Session, i *discordgo.InteractionCreate) {
	guildID := i.GuildID

	respond := func(title, desc string) {
		_ = discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{{
					Title:       title,
					Description: desc,
					Color:       0x1DB954,
				}},
			},
		})
	}

	if !GlobalQueue.IsInVoiceChannel(guildID) {
		respond("🔇 Not in Voice Channel", "I'm not currently in a voice channel.")
		return
	}

	if !GlobalQueue.IsPlaying(guildID) {
		respond("⏹️ Nothing Playing", "There's no track currently playing to skip.")
		return
	}

	SignalStop(guildID)
	SignalNewTrack(guildID)

	_ = discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{{
				Title:       "⏭️ Skipping Current Track",
				Description: "Skipping the current track and moving to the next one in the queue.",
				Color:       0x1DB954,
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Try /play to add new songs, or /help for all commands",
				},
			}},
		},
	})
}

func HandleSkipUserCommand(discord *discordgo.Session, i *discordgo.InteractionCreate) {
	guildID := i.GuildID
	channelID := i.ChannelID

	userOpt := i.ApplicationCommandData().Options[0]
	user := userOpt.UserValue(discord)
	if user == nil {
		discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ Unable to resolve the specified user.",
			},
		})
		return
	}
	userID := user.ID

	GlobalQueue.Lock()
	originalQueue := GlobalQueue.queues[channelID]
	filtered := make([]VideoInfo, 0, len(originalQueue))

	deletedFiles := 0
	for _, track := range originalQueue {
		if track.RequestedBy != userID {
			filtered = append(filtered, track)
			continue
		}
		if path, ok := GlobalQueue.GetDownloadedFile(track.Title); ok {
			if err := os.Remove(path); err == nil {
				deletedFiles++
			} else {
				log.Printf("⚠️ Failed to delete file %s: %v", path, err)
			}
		}
	}

	GlobalQueue.queues[channelID] = filtered

	removedCount := len(originalQueue) - len(filtered)
	current := GlobalQueue.currentlyPlaying[guildID]
	shouldSkipCurrent := current.RequestedBy == userID
	GlobalQueue.Unlock()

	description := fmt.Sprintf(
		"Removed %d track(s) requested by <@%s> from the queue.",
		removedCount, userID,
	)
	if deletedFiles > 0 {
		description += fmt.Sprintf("\n🗑️ Deleted %d audio file(s) from disk.", deletedFiles)
	}
	if shouldSkipCurrent {
		description += "\n\n⏭️ The current track will now be skipped."
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🚫 User Tracks Removed",
		Description: description,
		Color:       0x1DB954,
	}

	discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})

	if shouldSkipCurrent {
		// Use the new playback loop signaling system:
		go func() {
			SignalStop(guildID)     // stops current playback
			SignalNewTrack(guildID) // triggers playback loop to pick next track
		}()
	}
}

func HandleSkipUserAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	guildID := i.GuildID

	members := []*discordgo.Member{}
	after := ""
	limit := 1000

	for {
		chunk, err := s.GuildMembers(guildID, after, limit)
		if err != nil {
			break
		}
		if len(chunk) == 0 {
			break
		}
		members = append(members, chunk...)
		after = chunk[len(chunk)-1].User.ID
		if len(chunk) < limit {
			break
		}
	}

	sort.Slice(members, func(i, j int) bool {
		return strings.ToLower(members[i].User.Username) < strings.ToLower(members[j].User.Username)
	})

	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, member := range members {
		if member.User.Bot {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  member.User.Username,
			Value: member.User.ID,
		})
		if len(choices) >= 25 {
			break
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}
