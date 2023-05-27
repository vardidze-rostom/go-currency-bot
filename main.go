package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"kakafoni/database"
	"kakafoni/fiat_currency"
	"kakafoni/gold_price"
	"kakafoni/logic"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	token := os.Getenv("TELEGRAM_APITOKEN")
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Panic("Ошибка, нет катой переменной окужения: ", err)
	}

	debug_bot := os.Getenv("DEBUG_BOT")
	bot.Debug, err = strconv.ParseBool(debug_bot)
	if err != nil {
		log.Panic("Ошибка, нет катой переменной окужения: ", err)
	}

	// создаём или открываем файл БД
	db, err := gorm.Open(sqlite.Open("currencyes.db"), &gorm.Config{})
	if err != nil {
		log.Panic("failed to connect database")
	}

	err = database.CreateTable(db, &fiat_currency.FiatCurrency{})
	if err != nil {
		log.Panic("failed to migrate database", err)
	}

	err = database.CreateTable(db, &gold_price.GoldPriceMakhachkala{})
	if err != nil {
		log.Panic("failed to migrate database", err)
	}

	if fiat_currency.IsTableEmpty(db) {
		fiat_currency.ParseJsonIntoTable(db, fiat_currency.URL_TO_JSON_FIAT)
		log.Printf("\nТаблица fiat_currency была пустой\n")
	}
	if gold_price.IsTableEmpty(db) {
		gold_price.ParseGoldPriseMakhachkala(db)
		log.Printf("\nТаблица gold_price_makhachkala была пустой\n")
	}

	// запускаем cron задачу, которая выполняется каждую полночь
	location, _ := time.LoadLocation("Europe/Moscow")
	c := cron.New(cron.WithLocation(location))
	c.AddFunc("@midnight", func() {
		fiat_currency.ParseJsonIntoTable(db, fiat_currency.URL_TO_JSON_FIAT)
		gold_price.ParseGoldPriseMakhachkala(db)
		log.Printf("\nСработал крон\n")
	})
	c.Start()
 
	updateConfig := tgbotapi.NewUpdate(-1)
	updateConfig.Timeout = 60

	updates := bot.GetUpdatesChan(updateConfig)

	// машина состояний
	userFSMs := make(map[int64]*logic.UserFSM)

	fiatCurrencySlice := fiat_currency.CharCodes(db)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID

		userFSM, ok := userFSMs[chatID]
		if !ok {
			userFSM = logic.NewUserFSM(chatID)
			userFSMs[chatID] = userFSM
		}

		var event string

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")

		// защита от спама
		if userFSM.IsSpam() {
			if !userFSM.IsBlocked {
				msg.Text = fmt.Sprintf("Вы превысили лимит сообщений. Подождите пожалуйста %d секунды.", logic.CountSeconds)
				bot.Send(msg)
				userFSM.IsBlocked = true
				log.Printf("user: %d blocked", userFSM.ChatID)
			}
			continue
		} else {
			userFSM.TimeRequest = time.Now()
			userFSM.IsBlocked = false
		}

		switch update.Message.Command() {
		case logic.Start:
			event = logic.Start
			msg.ReplyMarkup, msg.Text = logic.MainMenu("Выберите операцию", logic.MainMenuKeyboard)
		case "help":
			msg.Text = "Бот для конвертирования валют. Если хотите венруться в меню выбора операций отправьте /cancel"
			bot.Send(msg)
			continue
		case "cancel":
			event = logic.Start
			userFSM.ChangeEvent(event)
			msg.ReplyMarkup, msg.Text = logic.MainMenu("Отмена операции", logic.MainMenuKeyboard)
			bot.Send(msg)
			continue
		default:
			event = ""
		}

		user_text := update.Message.Text
		switch userFSM.FSM.Current() {
		case logic.ChoiceOperation:
			switch user_text {
			case logic.MainMenuKeyboard_fiatCurrency:
				msg.ReplyMarkup = fiat_currency.CharCodesKeyboard(fiatCurrencySlice)
				event = logic.CourseFiatCurrency
				msg.Text = "Выберите валюту"
			case logic.MainMenuKeyboard_calculateFiatCyrrencyes:
				msg.ReplyMarkup = fiat_currency.CharCodesKeyboard(fiatCurrencySlice)
				event = logic.FirstFiatCyrrency
				msg.Text = "Выберите первую валюту"
			case logic.MainMenuKeyboard_goldMakhachkala:
				msg.Text = "Цена золота в Махачкале"
				bot.Send(msg)
				msg.Text, err = gold_price.HandleChoice(db)
				if err != nil {
					msg.Text, event = logic.ERROR_MESSAGE, ""
				}
			default:
				msg.Text = fiat_currency.INCORRECT_OPERATION
			}
		case logic.ChoiceOneFiatCurrency:
			_, err := fiat_currency.SelectFromTable(db, user_text)
			if err != nil {
				msg.Text, event = logic.ERROR_MESSAGE, ""
				bot.Send(msg)
				continue
			}
			msg.Text, event = fiat_currency.HandleChoice(fiatCurrencySlice, user_text, logic.Start)
			bot.Send(msg)
			if event == "" {
				continue
			}
			msg.ReplyMarkup, msg.Text = logic.MainMenu("Выберите операцию", logic.MainMenuKeyboard)
		case logic.ChoiceFirstFiatCurrency:
			_, err := fiat_currency.SelectFromTable(db, user_text)
			if err != nil {
				msg.Text, event = logic.ERROR_MESSAGE, ""
				bot.Send(msg)
				continue
			}
			msg.Text, event = fiat_currency.HandleChoice(fiatCurrencySlice, user_text, logic.SecondFiatCyrrency)
			bot.Send(msg)
			if event == "" {
				continue
			}

			userFSM.UserData["firstCurrencyCode"] = user_text
			msg.Text = "Выберите вторую валюту"
		case logic.ChoiceSecondFiatCurrency:
			_, err := fiat_currency.SelectFromTable(db, user_text)
			if err != nil {
				msg.Text, event = logic.ERROR_MESSAGE, ""
				bot.Send(msg)
				continue
			}
			msg.Text, event = fiat_currency.HandleChoice(fiatCurrencySlice, user_text, logic.FiatAmount)
			bot.Send(msg)
			if event == "" {
				continue
			}

			userFSM.UserData["secondCurrencyCode"] = user_text
			msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
			msg.Text = "Введите количество целым числом"
		case logic.ChoiceFiatAmount:
			amount, err := strconv.Atoi(user_text)
			if err != nil {
				msg.Text, event = fiat_currency.INCORRECT_NUMBER, ""
			} else {

				fist_fiatCurrency, err := fiat_currency.SelectFromTable(db, userFSM.UserData["firstCurrencyCode"])
				if err != nil {
					msg.Text, event = logic.ERROR_MESSAGE, ""
					bot.Send(msg)
					continue
				}

				second_fiatCurrency, err := fiat_currency.SelectFromTable(db, userFSM.UserData["secondCurrencyCode"])
				if err != nil {
					msg.Text, event = logic.ERROR_MESSAGE, ""
					bot.Send(msg)
					continue
				}

				answer, err := fiat_currency.ConvertCurrency(
					fist_fiatCurrency,
					second_fiatCurrency,
					amount,
				)
				if err != nil {
					msg.Text, event = fiat_currency.ERROR_CONVERT, ""
				} else {
					msg.Text, event = strconv.Itoa(amount)+" "+userFSM.UserData["firstCurrencyCode"]+" = "+answer+" "+userFSM.UserData["secondCurrencyCode"], logic.Start
					bot.Send(msg)
					msg.ReplyMarkup, msg.Text = logic.MainMenu("Выберите операцию", logic.MainMenuKeyboard)
				}
			}
		}

		userFSM.ChangeEvent(event)
		bot.Send(msg)
	}
}
