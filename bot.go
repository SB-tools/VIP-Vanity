package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/log"
	"github.com/disgoorg/snowflake/v2"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
)

var (
	metadataApiURL = "https://api.cloudflare.com/client/v4/accounts/6dccc8a823380a32fe8792904b2cd886/storage/kv/namespaces/b44b93e4cc174443aca099a3763b29ff/metadata/"
	valuesApiURL   = "https://api.cloudflare.com/client/v4/accounts/6dccc8a823380a32fe8792904b2cd886/storage/kv/namespaces/b44b93e4cc174443aca099a3763b29ff/values/"
	cfApiToken     = os.Getenv("CF_API_TOKEN")
	publicIDRegex  = regexp.MustCompile(`^[a-f0-9]+$`)
	vanityRegex    = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
)

func main() {
	log.SetLevel(log.LevelInfo)
	log.Info("starting the bot...")
	log.Info("disgo version: ", disgo.Version)

	client, err := disgo.New(os.Getenv("VIP_VANITY_TOKEN"),
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentsNone),
			gateway.WithPresenceOpts(gateway.WithWatchingActivity("VIPs"))),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagsNone)),
		bot.WithEventListeners(&events.ListenerAdapter{
			OnApplicationCommandInteraction: onCommand,
		}))
	if err != nil {
		log.Fatal("error while building disgo instance: ", err)
	}

	defer client.Close(context.TODO())

	if err := client.OpenGateway(context.TODO()); err != nil {
		log.Fatal("error while connecting to the gateway: ", err)
	}

	log.Info("vip vanity bot is now running.")
	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-s
}

func onCommand(event *events.ApplicationCommandInteractionCreate) {
	data := event.SlashCommandInteractionData()
	pubUserID := data.String("sb_user_id")
	if !publicIDRegex.MatchString(pubUserID) {
		_ = event.CreateMessage(discord.MessageCreate{
			Content: "Provided user id is not a valid public user id.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	vanity := strings.ToLower(data.String("vanity"))
	if !vanityRegex.MatchString(vanity) {
		_ = event.CreateMessage(discord.MessageCreate{
			Content: "Provided vanity is not in a valid format. Use letters and numbers only up to 32 characters.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	_ = event.DeferCreateMessage(true)
	metaRequest, err := http.NewRequest(http.MethodGet, metadataApiURL+vanity, nil)
	if err != nil {
		log.Error("there was an error while creating a new metadata request: ", err)
		return
	}
	metaRequest.Header.Add("Authorization", cfApiToken)

	client := event.Client().Rest().HTTPClient()
	metaRs, err := client.Do(metaRequest)
	if err != nil {
		log.Error("there was an error while running a metadata request: ", err)
		return
	}
	userID := event.User().ID
	if metaRs.StatusCode == http.StatusOK {
		defer metaRs.Body.Close()
		var response MetadataResponse
		if err = json.NewDecoder(metaRs.Body).Decode(&response); err != nil {
			log.Errorf("there was an error while decoding the metadata response (%d): ", metaRs.StatusCode, err)
			return
		}
		ownerID := response.Result.ID
		if ownerID != userID {
			createFollowup(event, "This vanity is already taken by <@%d>.", ownerID)
			return
		}
	}
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	w.WriteField("value", pubUserID)
	w.WriteField("metadata", fmt.Sprintf(`{"id":"%s"}`, userID))
	w.Close()
	valueRequest, err := http.NewRequest(http.MethodPut, valuesApiURL+vanity, buf)
	if err != nil {
		log.Error("there was an error while creating a new value request: ", err)
		return
	}
	valueRequest.Header.Add("Authorization", cfApiToken)
	valueRequest.Header.Add("Content-Type", w.FormDataContentType())
	valueRs, err := client.Do(valueRequest)
	if err != nil {
		log.Error("there was an error while running a value request: ", err)
		return
	}
	code := valueRs.StatusCode
	if code == http.StatusOK {
		createFollowup(event, "Vanity `%s` associated with user id [`%s`](https://sb.ltn.fi/userid/%[2]s) has been successfully added.", vanity, pubUserID)
	} else {
		log.Warnf("received code %d after running a value request", code)
	}
}

func createFollowup(event *events.ApplicationCommandInteractionCreate, s string, a ...any) {
	_, _ = event.Client().Rest().CreateFollowupMessage(event.ApplicationID(), event.Token(), discord.MessageCreate{
		Content: fmt.Sprintf(s, a...),
	})
}

type MetadataResponse struct {
	Result struct {
		ID snowflake.ID `json:"id"`
	} `json:"result"`
}
