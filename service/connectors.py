import service.logger
import service.configs
import service.sys_manager
import requests
import time
import os

class SendingData(service.sys_manager.ResourceManagement):
    def __init__(self):
        super().__init__()
        self.url_list = self.config.get("sending_data")["url_list"]
        self.delay = None

        try: self.max_attempts = int(self.config.get("sending_data", {}).get("max_attempts", 5))
        except Exception: self.max_attempts = 5


    def authentication_data(self, url_value, api_key_value, index):
        try:
            url = self.decrypt_data(url_value)
            api_key = self.decrypt_data(api_key_value)
            service.logger.logger_service.info(
                f"Пользовательские данные для подключения к API сервера [{index}] успешно расшифрованы")
            return url, api_key
        except Exception:
            service.logger.logger_service.error("Не удалось дешифровать данные для подключения к API", exc_info=True)

    def send_fiscals_data(self):
        try:
            all_json_files = [f for f in os.listdir(self.date_path) if f.endswith('.json')]
            service.logger.logger_service.info("Получен список файлов для отправки на сервер")
            service.logger.logger_service.debug(all_json_files)

            for index, item in enumerate(self.url_list):
                encryption_value = item['encryption']
                url_value = item['url']
                api_key_value = item['api_key']

                service.logger.logger_service.debug(f"Получены данные для подключения к API сервера [{index}]: {item}")
                if encryption_value == True:
                    url_value, api_key_value = self.authentication_data(url_value, api_key_value, index)
                else:
                    service.logger.logger_service.warning(
                        f"Шифрование пользовательских данных для подключения к API сервера [{index}] отключено")

                for json_files in all_json_files:
                    json_data = service.configs.read_config_file(self.date_path, json_files, "")

                    try: self.delay = int(self.config.get("sending_data", {}).get("delay", 10))
                    except Exception: self.delay = 10

                    for attempt in range(self.max_attempts):
                        try:
                            service.logger.logger_service.info(
                                f"Делаем запрос к API-сервера [{index}] на отправку '{json_files}'")
                            service.logger.logger_service.debug(json_data)

                            # Заголовки
                            headers = {
                                'Content-Type': 'application/json',
                                'X-API-Key': api_key_value
                            }

                            # Отправка запроса
                            response = requests.post(url_value, headers=headers, json=json_data, timeout=(15, 60))

                            # Проверка ответа
                            service.logger.logger_service.info(f"Статус ответа: {response.status_code}")
                            service.logger.logger_service.debug(f"Ответ: {response.json()}")
                            break
                        except Exception:
                            current_attempt = attempt + 1
                            if attempt < self.max_attempts - 1:
                                service.logger.logger_service.warning(
                                    f"Попытка ({current_attempt}) отправить данные не удалась. "
                                    f"Повторная попытка через ({self.delay}) секунд")
                                time.sleep(self.delay)
                                self.delay *= 2
                                continue
                            service.logger.logger_service.error(
                                f"Не удалось отправить {json_files} на сервер после ({self.max_attempts}) попыток",
                                exc_info=True)
        except Exception:
            service.logger.logger_service.error(
                f"Попытка отправить данные на сервер завершилась неудачей", exc_info=True)


class TelegramNotification(service.sys_manager.ResourceManagement):
    def __init__(self):
        super().__init__()
        self.bot_token = self.config.get("notification")["authentication"]["bot_token"]
        self.chat_id = self.config.get("notification")["authentication"]["chat_id"]

        try: self.encryption_enabled = int(self.config["notification"]["authentication"].get("encryption", 1))
        except Exception: self.encryption_enabled = 1

        try: self.max_attempts = int(self.config["notification"].get("max_attempts", 5))
        except Exception: self.max_attempts = 5

        try: self.delay = int(self.config["notification"].get("delay", 10))
        except Exception: self.delay = 10

    def authentication_data(self):
        try:
            if self.encryption_enabled == True:
                self.bot_token = self.decrypt_data(self.bot_token)
                self.chat_id = self.decrypt_data(self.chat_id)
                service.logger.logger_service.info(
                    "Пользовательские данные для отправки уведомлений в Telegram успешно расшифрованы")
                return
            service.logger.logger_service.warning(
                "Шифрование пользовательских данных для отправки уведомлений в Telegram отключено")
        except Exception:
            service.logger.logger_service.error("Не удалось дешифровать данные для подключения к боту", exc_info=True)

    def request_tg_message(self, full_message):
        service.logger.logger_service.info("Сделан запрос на отправку уведомления в Telegram")
        service.logger.logger_service.debug(f"\n\n{full_message}")
        for attempt in range(self.max_attempts):
            try:
                url = f"https://api.telegram.org/bot{self.bot_token}/sendMessage"
                params = {
                    "chat_id": self.chat_id,
                    "text": full_message
                }

                response = requests.get(url, params=params, timeout=(15, 60))
                result = response.json()

                # Вывод полного ответа API
                service.logger.logger_service.debug("Ответ от API Telegram:")
                service.logger.logger_service.debug(result)

                # Проверка статуса отправки
                if result.get('ok'):
                    service.logger.logger_service.info("Уведомление в Telegram успешно отправлено")
                else:
                    service.logger.logger_service.error(f"Не удалось отправить уведомление: {result.get('description')}")

                return result
            except Exception:
                current_attempt = attempt + 1
                if attempt < self.max_attempts - 1:
                    service.logger.logger_service.warning(
                        f"Попытка ({current_attempt}) отправить уведомление не удалась. "
                        f"Повторная попытка через ({self.delay}) секунд")
                    time.sleep(self.delay)
                    self.delay *= 2
                    continue
                service.logger.logger_service.error(
                    f"Не удалось сделать запрос к API Telegram после ({self.max_attempts}) попыток", exc_info=True)

    def send_tg_message(self, message):
        import getdata.get_remote

        get_remote_id = getdata.get_remote.AnyRemoteId()

        try:
            url_rms = get_remote_id.get_server_url()
            teamviever_id = get_remote_id.get_teamviewer_id()
            anydesk_id = get_remote_id.get_anydesk_id()
            litemanager_id = get_remote_id.get_litemanager_id()

            full_message = (f"{message}\n\n"
                       f"RMS: {url_rms}\n"
                       f"TV: {teamviever_id}\n"
                       f"AD: {anydesk_id}\n"
                       f"LM: {litemanager_id}\n")

            self.request_tg_message(full_message)
        except Exception:
            service.logger.logger_service.error("Не удалось сформировать сообщение для отправки уведомления",
                                                exc_info=True)
