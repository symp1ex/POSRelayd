import service.logger
import service.configs
import service.connectors
import getdata.get_remote
import getdata.mitsu
import service.sys_manager
from getdata.atol.atol import get_driver_version
import about
import re
import os
import win32event
from datetime import datetime, timedelta

mitsu = getdata.mitsu.MitsuGetData()
tg_notification = service.connectors.TelegramNotification()
sending_data = service.connectors.SendingData()
get_remote_id = getdata.get_remote.AnyRemoteId()


class ValidationFn(service.sys_manager.ProcessManagement):
    def __init__(self):
        super().__init__()
        self.mask_name = None
        self.logs_dir = None
        self.serialNumber_key = None
        self.fnNumber_key = None
        self.target_time = self.config.get("validation_fn", {}).get("target_time", "05:30")

        try: self.validation = int(self.config.get("validation_fn", {}).get("enabled", 1))
        except: self.validation = 1

        try: self.trigger_days = int(self.config.get("validation_fn", {}).get("trigger_days", 3))
        except: self.trigger_days = 3

        try: self.interval_in_hours = int(self.config.get("validation_fn", {}).get("interval", 12))
        except: self.interval_in_hours = 12

        try: self.forced = int(self.config.get("validation_fn", {}).get("forced", 0))
        except: self.forced = 0

        self.reboot_file = self.config.get("validation_fn", {}).get("reboot_file", "reboot.bat")

        self.time_sleep_ms = None
        self.hh = None
        self.mm = None

    def check_validation_date(self, i, model_kkt):
        try:
            try:
                serialNumber = self.fiscals_data.get(model_kkt)[i]["serialNumber"]
                validation_date = self.fiscals_data.get(model_kkt)[i]["v_time"]

                json_name = f"{serialNumber}.json"
                json_path = os.path.join(about.current_path, "date", json_name)
                json_file = service.configs.read_config_file(about.current_path, json_path, "",
                                                             create=False)
            except Exception:
                service.logger.logger_service.error(f"Не удалось получить значение запрашиваемого ключа из конфига",
                                                    exc_info=True)
                return

            get_current_time = self.current_time()

            service.logger.logger_service.info(f"Будет произведена валидация ФР №{serialNumber}")
            service.logger.logger_service.debug(f"Дата последней валидации: {validation_date}")
            service.logger.logger_service.debug(f"Количество дней, после которого валидация считается не пройденной: "
                                                f"{self.trigger_days}")

            difference_in_days = (datetime.strptime(get_current_time, "%Y-%m-%d %H:%M:%S") -
                                  datetime.strptime(validation_date, "%Y-%m-%d %H:%M:%S")).days
            valid = difference_in_days < self.trigger_days
            service.logger.logger_service.info(f"Результат валидации ФР №{serialNumber}: '{valid}'")

            try:
                if model_kkt == "atol":
                    json_file["installed_driver"] = get_driver_version()
                elif model_kkt == "mitsu":
                    json_file["installed_driver"] = str(mitsu.get_driver_version())

                if "rustdesk_id" not in json_file:
                    json_file["rustdesk_id"] = str(get_remote_id.get_rustdesk_id())

                json_file["url_rms"] = get_remote_id.get_server_url()
                json_file["vc"] = about.version
                json_file["uuid"] = self.get_uuid()
                service.configs.write_json_file(json_file, json_path)
            except Exception:
                service.logger.logger_service.warn(f"Не удалось обновить '{os.path.abspath(json_path)}'", exc_info=True)

            return valid
        except Exception:
            service.logger.logger_service.error(f"Не удалось вычислить разницу между текущей датой и датой последней "
                                                f"валидации ФН.", exc_info=True)

    def calc_time_before_reboot(self):
        if self.target_time == 0:
            self.hh = 0
            self.mm = 0
            return

        time_sleep = self.get_seconds_until_next_time()
        self.time_sleep_ms = time_sleep * 1000
        self.hh = int(time_sleep / 3600)
        self.mm = int((time_sleep % 3600) / 60)

    def get_seconds_until_next_time(self):
        try:
            # Получаем текущую дату и время
            current = datetime.strptime(self.current_time(), "%Y-%m-%d %H:%M:%S")

            try:
                # Разбиваем целевое время на часы и минуты
                target_hour, target_minute = map(int, self.target_time.split(':'))
            except Exception:
                service.logger.logger_service.warn("Не удалось получить из конфига время следующей перезагрузки, "
                                                   "будет использовано дефолтное значение", exc_info=True)
                target_hour = 5
                target_minute = 30
            service.logger.logger_service.debug(f"Получено время следующей перезагрузки: {target_hour}:"
                                                f"{target_minute}")

            # Создаем новую дату с заменой времени на целевое
            target_datetime = current.replace(hour=target_hour, minute=target_minute, second=0, microsecond=0)

            # Вычисляем разницу в секундах
            difference = (target_datetime - current).total_seconds()

            if difference != abs(difference):
                # Создаем дату на следующий день
                next_day = current + timedelta(days=1)
                target_datetime = next_day.replace(hour=target_hour, minute=target_minute, second=0, microsecond=0)
                difference = (target_datetime - current).total_seconds()
                service.logger.logger_service.debug(f"Рассчитано время до следующей перезагрузки: {difference} сек.")
                return int(difference)

            service.logger.logger_service.debug(f"Рассчитано время до следующей перезагрузки: {difference} сек.")
            return int(difference)
        except Exception:
            service.logger.logger_service.error(f"Не удалось вычислить дату для перезагрузки", exc_info=True)

    def check_fiscal_register(self, i, model_kkt):
        get_current_time = self.current_time()

        if model_kkt == "atol":
            self.mask_name = self.config.get("validation_fn")["atol"]['logs_mask_name']
            self.logs_dir = self.config.get("validation_fn")["atol"]['logs_dir']
            self.serialNumber_key = self.config.get("validation_fn")["atol"]['serialNumber_key']
            self.fnNumber_key = self.config.get("validation_fn")["atol"]['fnNumber_key']
        elif model_kkt == "mitsu":
            self.mask_name = self.config.get("validation_fn")["mitsu"]['logs_mask_name']
            self.logs_dir = self.config.get("validation_fn")["mitsu"]['logs_dir']
            self.serialNumber_key = self.config.get("validation_fn")["mitsu"]['serialNumber_key']
            self.fnNumber_key = self.config.get("validation_fn")["mitsu"]['fnNumber_key']
        else:
            service.logger.logger_service.debug(f"Для модели ККТ '{model_kkt}' нет подходящих для проверки логов")
            return "skip"

        # Получаем значения из JSON
        try:
            serial_number = self.fiscals_data.get(model_kkt)[i]['serialNumber']
            fn_serial = self.fiscals_data.get(model_kkt)[i]['fn_serial']
            validation_date = self.fiscals_data.get(model_kkt)[i]["v_time"]
        except Exception:
            service.logger.logger_service.error(f"Не удалось получить значение запрашиваемого ключа из конфига",
                                                exc_info=True)
            return "skip"

        if self.logs_dir == "iiko":
            target_folder_path = "iiko\\cashserver"

            if get_remote_id.user_appdata == None:
                get_remote_id.get_user_appdata(target_folder_path)

            self.logs_dir = os.path.join(str(get_remote_id.user_appdata), 'iiko', 'Cashserver', 'logs')

        if not os.path.exists(self.logs_dir):
            service.logger.logger_service.warning(
                f"Путь до папки с логами: '{self.logs_dir}' не найден, невозможно провести валидацию")
            disable_check_fr = self.disable_check_fr(get_current_time, validation_date, serial_number, i, model_kkt)

            if self.notification_enabled == True and disable_check_fr == True:
                service.logger.logger_service.info("Уведомление об удалении будет отправлено в ТГ")
                message = (f"Не удалось проверить ФР №{serial_number} более '{self.delete_days}' дней, "
                           f"дальнейшая проверка будет отключена до следующего успешного подключения к ФР.")
                tg_notification.send_tg_message(message)
            return "skip"

        try:
            # Находим все подходящие файлы
            log_files = [
                os.path.join(self.logs_dir, filename)
                for filename in os.listdir(self.logs_dir)
                if self.mask_name in filename and filename.endswith('.log')
            ]

            if not log_files:
                service.logger.logger_service.warning(f"Файл лога, содержащий в названии '%{self.mask_name}%' "
                                                      f"не найден, невозможно провести валидацию")
                disable_check_fr = self.disable_check_fr(get_current_time, validation_date, serial_number, i, model_kkt)

                if self.notification_enabled == True and disable_check_fr == True:
                    service.logger.logger_service.info("Уведомление об удалении будет отправлено в ТГ")
                    message = (f"Не удалось проверить ФР №{serial_number} более '{self.delete_days}' дней, "
                               f"дальнейшая проверка будет отключена до следующего успешного подключения к ФР.")
                    tg_notification.send_tg_message(message)
                return "skip"

            service.logger.logger_service.debug(f"Найденные следующие файлы логов по пути: '{self.logs_dir}'")
            for log_file in log_files:
                service.logger.logger_service.debug(log_file)

            log_days_update = (datetime.strptime(get_current_time, "%Y-%m-%d %H:%M:%S") -
                               timedelta(days=self.trigger_days))

            # Фильтруем файлы, оставляя только те, которые не старше trigger_days дней
            recent_files = [
                f for f in log_files
                if datetime.fromtimestamp(os.path.getmtime(f)) > log_days_update
            ]

            if not recent_files:
                service.logger.logger_service.warning(f"Не найдено логов, которые обновлялись бы менее "
                                                      f"'{self.trigger_days}' дней назад")
                disable_check_fr = self.disable_check_fr(get_current_time, validation_date, serial_number, i, model_kkt)

                if self.notification_enabled == True and disable_check_fr == True:
                    service.logger.logger_service.info("Уведомление об удалении будет отправлено в ТГ")
                    message = (f"Не удалось проверить ФР №{serial_number} более '{self.delete_days}' дней, "
                               f"дальнейшая проверка будет отключена до следующего успешного подключения к ФР.")
                    tg_notification.send_tg_message(message)
                return "skip"

            # Находим файл с самой поздней датой изменения
            latest_file = max(recent_files, key=os.path.getmtime)
            service.logger.logger_service.info(f"Будет произведён поиск ФР №{serial_number} по файлу: '{latest_file}'")

            if model_kkt == "atol":
                # Регулярка для поиска нужной строки в логе Atol
                pattern = re.compile(
                    rf'{re.escape(self.serialNumber_key)}(\d+),.*?{re.escape(self.fnNumber_key)}(\d+)\b'
                )

                log_serial = None
                log_fn = None

                with open(latest_file, 'r', encoding='utf-8') as f:
                    for line in f:
                        match = pattern.search(line)
                        if match:
                            log_serial = match.group(1)
                            log_fn = match.group(2)

                            if log_serial == serial_number:
                                return self.check_fiscal_register_match_result(log_fn, fn_serial, serial_number,
                                                                          get_current_time, i, model_kkt)

                service.logger.logger_service.warning(
                    f"Запись об ФР №{serial_number}, не найдена в файле '{latest_file}'")
                disable_check_fr = self.disable_check_fr(get_current_time, validation_date, serial_number, i, model_kkt)

                if self.notification_enabled == True and disable_check_fr == True:
                    service.logger.logger_service.info("Уведомление об удалении будет отправлено в ТГ")
                    message = (f"Не удалось проверить ФР №{serial_number} более '{self.delete_days}' дней, "
                               f"дальнейшая проверка будет отключена до следующего успешного подключения к ФР.")
                    tg_notification.send_tg_message(message)
                return "skip"

            elif model_kkt == "mitsu":
                # Для Mitsu используем другой подход - ищем блок данных, относящийся к нужному ФР
                serial_pattern = re.compile(rf'{re.escape(self.serialNumber_key)}(\d+)')
                fn_pattern = re.compile(rf'{re.escape(self.fnNumber_key)}(\d+)')

                with open(latest_file, 'r', encoding='utf-8') as f:
                    # Флаг, который показывает, что мы нашли серийный номер ФР и ищем соответствующий номер ФН
                    found_serial = False
                    log_serial = None
                    log_fn = None

                    for line in f:
                        if not found_serial:
                            # Ищем строку с серийным номером ФР
                            match_serial = serial_pattern.search(line)
                            if match_serial:
                                potential_log_serial = match_serial.group(1)
                                if potential_log_serial == serial_number:
                                    log_serial = potential_log_serial
                                    found_serial = True
                                    service.logger.logger_service.debug(f"Найден серийный номер ФР: {log_serial}")
                                    # Продолжаем цикл для поиска номера ФН
                        else:
                            # Уже нашли серийный номер, теперь ищем ближайший номер ФН
                            match_fn = fn_pattern.search(line)
                            if match_fn:
                                log_fn = match_fn.group(1)
                                service.logger.logger_service.debug(f"Найден номер ФН: {log_fn}")
                                # Нашли номер ФН, можно прекратить поиск
                                return self.check_fiscal_register_match_result(log_fn, fn_serial, serial_number,
                                                                               get_current_time, i, model_kkt)

                            # Если мы нашли новый серийный номер до того, как нашли номер ФН,
                            # это значит, что для текущего ФР номер ФН не указан или начался новый блок
                            new_match_serial = serial_pattern.search(line)
                            if new_match_serial and new_match_serial.group(1) != log_serial:
                                service.logger.logger_service.warning(
                                    f"Найден новый серийный номер, но не найден номер ФН для ФР №{serial_number}")
                                found_serial = False

                service.logger.logger_service.warning(
                    f"Запись об ФР №{serial_number} или соответствующем ФН не найдена в файле '{latest_file}'")
                disable_check_fr = self.disable_check_fr(get_current_time, validation_date, serial_number, i, model_kkt)

                if self.notification_enabled == True and disable_check_fr == True:
                    service.logger.logger_service.info("Уведомление об удалении будет отправлено в ТГ")
                    message = (f"Не удалось проверить ФР №{serial_number} более '{self.delete_days}' дней, "
                               f"дальнейшая проверка будет отключена до следующего успешного подключения к ФР.")
                    tg_notification.send_tg_message(message)
                return "skip"

        except Exception:
            service.logger.logger_service.error(f"Неизвестная ошибка при парсинге лога, мне жаль ;(", exc_info=True)
            return "skip"

    def check_fiscal_register_match_result(self, log_fn, fn_serial, serial_number, get_current_time, i, model_kkt):
        service.logger.logger_service.info(f"Соответствие ФР и ФН проверено: '{log_fn == fn_serial}'")

        if log_fn == fn_serial:
            json_name = f"{serial_number}.json"
            try:
                json_path = os.path.join(about.current_path, "date", json_name)
                json_file = service.configs.read_config_file(about.current_path, json_path, "", create=False)

                json_file["v_time"] = get_current_time
                service.configs.write_json_file(json_file, json_path)
            except Exception:
                service.logger.logger_service.error(f"Не удалось обновить '{json_name}'", exc_info=True)

            self.fiscals_data[model_kkt][i]["v_time"] = get_current_time
            service.configs.write_json_file(self.fiscals_data, self.fiscals_file)
            return True

        service.logger.logger_service.info(f"Для ФР№{serial_number}, актуальным является ФН №{log_fn}")

        self.calc_time_before_reboot()

        if self.notification_enabled == True:
            service.logger.logger_service.info("Уведомление о не соответствии будет отправлено в ТГ")
            message = (f"ФР №{serial_number} больше не соответствует ФН №{fn_serial}, "
                       f"актуальный для него ФН №{log_fn}.\nСистема будете перезагружена "
                       f"через {self.hh}ч. {self.mm}м.")
            tg_notification.send_tg_message(message)
        return False

    def fn_check_process(self, service_instance):
        self.get_fiscals_json()

        if self.notification_enabled == True:
            tg_notification.authentication_data()

        interval = 3600000 * self.interval_in_hours

        try:
            while service_instance.is_running:
                reboot_flag = 0
                break_flag = 0

                for model_kkt in self.fiscals_data.keys():
                    if break_flag == 1:
                        break

                    if not self.fiscals_data.get(model_kkt):
                        service.logger.logger_service.debug(f"ККТ модели '{model_kkt}' отсутствуют в базе")
                        continue

                    service.logger.logger_service.info(f"Проверяем ККТ модели: '{model_kkt}'")

                    for i in range(len(self.fiscals_data.get(model_kkt))):
                        result_validation = self.check_validation_date(i, model_kkt)
                        if result_validation == False:
                            if self.forced == 1:
                                self.calc_time_before_reboot()
                                break_flag = 1
                                reboot_flag = 1
                                break
                            service.logger.logger_service.info(f"По логам будет произведено сопоставление ФР и ФН")
                            result_correlation = self.check_fiscal_register(i, model_kkt)
                            if result_correlation == False:
                                reboot_flag = 1

                    self.remove_empty_serials_from_file(model_kkt)

                if sending_data.sending_data_enabled == True:
                    service.logger.logger_service.info("Производится отправка данных на сервер")
                    sending_data.send_fiscals_data()

                if reboot_flag == 0:
                    if self.updater_enabled == True:
                        process_not_found = self.check_process_cycle(self.updater_name, kill_process=True)
                        if process_not_found:
                            self.subprocess_run("updater", self.updater_name)
                else:
                    if self.target_time == 0:
                        self.subprocess_run("_resources", self.reboot_file)
                    else:
                        service.logger.logger_service.info(f"Через {self.hh}ч.{self.mm}м. будет запущен файл "
                                                           f"'{self.reboot_file}'")

                        rc = win32event.WaitForSingleObject(service_instance.hWaitStop, self.time_sleep_ms)
                        if rc == win32event.WAIT_OBJECT_0:
                            break
                        self.subprocess_run("_resources", self.reboot_file)
                service.logger.logger_service.info(f"До следующей проверки осталось {self.interval_in_hours} часов")
                rc = win32event.WaitForSingleObject(service_instance.hWaitStop, interval)
                if rc == win32event.WAIT_OBJECT_0:
                    break
        except Exception:
            service.logger.logger_service.critical(f"Произошло нештатное прерывание основного потока службы",
                                    exc_info=True)
            os._exit(1)
