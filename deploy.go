package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

const (
	selectVersionAction     = "select-version"
	confirmDeploymentAction = "confirm-deployment"
)

func main() {
	api := slack.New(os.Getenv("SLACK_BOT_TOKEN"))

	http.HandleFunc("/slack/events", slackVerificationMiddleware(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		eventsAPIEvent, err := slackevents.ParseEvent(body, slackevents.OptionNoVerifyToken())
		if err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		switch eventsAPIEvent.Type {
		case slackevents.URLVerification:
			var res *slackevents.ChallengeResponse
			if err := json.Unmarshal(body, &res); err != nil {
				log.Println(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			if _, err := w.Write([]byte(res.Challenge)); err != nil {
				log.Println(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		case slackevents.CallbackEvent:
			innerEvent := eventsAPIEvent.InnerEvent
			switch event := innerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				message := strings.Split(event.Text, " ")
				if len(message) < 2 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				command := message[1]
				switch command {
				case "deploy":
					text := slack.NewTextBlockObject(slack.MarkdownType, "Please select *version*.", false, false)
					textSection := slack.NewSectionBlock(text, nil, nil)

					versions := []string{"v1.0.0", "v1.1.0", "v1.1.1"}
					options := make([]*slack.OptionBlockObject, 0, len(versions))
					for _, v := range versions {
						optionText := slack.NewTextBlockObject(slack.PlainTextType, v, false, false)
						options = append(options, slack.NewOptionBlockObject(v, optionText, optionText))
					}

					placeholder := slack.NewTextBlockObject(slack.PlainTextType, "Select version", false, false)
					selectMenu := slack.NewOptionsSelectBlockElement(slack.OptTypeStatic, placeholder, "", options...)

					actionBlock := slack.NewActionBlock(selectVersionAction, selectMenu)

					fallbackText := slack.MsgOptionText("This client is not supported.", false)
					blocks := slack.MsgOptionBlocks(textSection, actionBlock)

					if _, err := api.PostEphemeral(event.Channel, event.User, fallbackText, blocks); err != nil {
						log.Println(err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
			}
		}
	}))

	http.HandleFunc("/slack/actions", slackVerificationMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var payload *slack.InteractionCallback
		if err := json.Unmarshal([]byte(r.FormValue("payload")), &payload); err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		switch payload.Type {
		case slack.InteractionTypeBlockActions:
			if len(payload.ActionCallback.BlockActions) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			action := payload.ActionCallback.BlockActions[0]
			switch action.BlockID {
			case selectVersionAction:
				version := action.SelectedOption.Value

				text := slack.NewTextBlockObject(slack.MarkdownType,
					fmt.Sprintf("Could I deploy `%s`?", version), false, false)
				textSection := slack.NewSectionBlock(text, nil, nil)

				confirmButtonText := slack.NewTextBlockObject(slack.PlainTextType, "Do it", false, false)
				confirmButton := slack.NewButtonBlockElement("", version, confirmButtonText)
				confirmButton.WithStyle(slack.StylePrimary)

				denyButtonText := slack.NewTextBlockObject(slack.PlainTextType, "Stop", false, false)
				denyButton := slack.NewButtonBlockElement("", "deny", denyButtonText)
				denyButton.WithStyle(slack.StyleDanger)

				actionBlock := slack.NewActionBlock(confirmDeploymentAction, confirmButton, denyButton)

				fallbackText := slack.MsgOptionText("This client is not supported.", false)
				blocks := slack.MsgOptionBlocks(textSection, actionBlock)

				replaceOriginal := slack.MsgOptionReplaceOriginal(payload.ResponseURL)
				if _, _, _, err := api.SendMessage("", replaceOriginal, fallbackText, blocks); err != nil {
					log.Println(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			case confirmDeploymentAction:
				if strings.HasPrefix(action.Value, "v") {
					version := action.Value
					go func() {
						startMsg := slack.MsgOptionText(
							fmt.Sprintf("<@%s> OK, I will deploy `%s`.", payload.User.ID, version), false)
						if _, _, err := api.PostMessage(payload.Channel.ID, startMsg); err != nil {
							log.Println(err)
						}

						deploy(version)

						endMsg := slack.MsgOptionText(
							fmt.Sprintf("`%s` deployment completed!", version), false)
						if _, _, err := api.PostMessage(payload.Channel.ID, endMsg); err != nil {
							log.Println(err)
						}
					}()
				}

				deleteOriginal := slack.MsgOptionDeleteOriginal(payload.ResponseURL)
				if _, _, _, err := api.SendMessage("", deleteOriginal); err != nil {
					log.Println(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
		}
	}))

	log.Println("[INFO] Server listening")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func slackVerificationMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verifier, err := slack.NewSecretsVerifier(r.Header, os.Getenv("SLACK_SIGNING_SECRET"))
		if err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		bodyReader := io.TeeReader(r.Body, &verifier)
		body, err := ioutil.ReadAll(bodyReader)
		if err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if err := verifier.Ensure(); err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

		next.ServeHTTP(w, r)
	}
}

func deploy(version string) {
	log.Printf("deploy %s", version)
	time.Sleep(10 * time.Second)
}
