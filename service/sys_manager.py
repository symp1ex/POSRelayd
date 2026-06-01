import service.logger
import service.configs
import about
import os
import subprocess
import socket
import time
import winreg
import uuid
import hashlib
from datetime import datetime
import win32api
import shutil
from cryptography.fernet import Fernet

rm_date_flag = 0

class ResourceManagement:
    crypto_key = b't_qxC_HN04Tiy1ish2P27ROYSJt_m7_FE2JT6gYngOM='
    config_file = "service.json"
    resource_path = "_resources"
    date_path = os.path.join(about.current_path, "date")
    fiscals_file = os.path.join(about.current_path, resource_path, "fiscals.json")
    uuid_file = os.path.join(about.current_path, resource_path, "uuid")
    uuid = None

    def __init__(self):
        self.config = service.configs.read_config_file(about.current_path, self.config_file,
                                                       service.configs.service_data, create=True)

        self.config_connect = service.configs.read_config_file(about.current_path, "connect.json",
                                                               service.configs.connect_data, create=True)

        try: self.updater_enabled = int(self.config.get("service", {}).get("updater", {}).get("enabled", 1))
        except: self.updater_enabled = 0

        self.updater_name = self.config.get("service", {}).get("updater", {}).get("file_name", "updater.exe")

        try: self.sending_data_enabled = int(self.config.get("sending_data", {}).get('enabled', 0))
        except: self.sending_data_enabled = 0

        try: self.notification_enabled = int(self.config.get("notification", {}).get('enabled', 0))
        except: self.notification_enabled = 0

        try: self.delete_days = int(self.config.get("validation_fn", {}).get("delete_days", 21))
        except: self.delete_days = 21

        self.fiscals_data = None

    def get_fiscals_json(self):
        self.fiscals_data = service.configs.read_config_file(about.current_path, self.fiscals_file,
                                                             {"atol":[],"mitsu":[],"shtrih":[]}, create=True)

    def decrypt_data(self, encrypted_data):
        try:
            cipher = Fernet(self.crypto_key)
            decrypted_data = cipher.decrypt(encrypted_data).decode()
            return decrypted_data
        except Exception:
            service.logger.logger_service.error("Не удалось дешифровать данные", exc_info=True)

    def encrypt_data(self, data):
        try:
            cipher = Fernet(self.crypto_key)
            encrypted_data = cipher.encrypt(data.encode())
            return encrypted_data
        except Exception:
            service.logger.logger_service.error("Не удалось зашифровать данные", exc_info=True)

    def update_correlation_fiscals(self, serialNumber, fn_serial, get_current_time, model_kkt):
        self.get_fiscals_json()

        try:
            # Проверка наличия ключа "fiscals" и добавление новой записи
            if model_kkt not in self.fiscals_data:
                self.fiscals_data[model_kkt] = []

            existing_entry = next((item for item in self.fiscals_data[model_kkt]
                                   if item["serialNumber"] == serialNumber), None)

            if existing_entry:
                # Обновление существующего элемента
                existing_entry["fn_serial"] = fn_serial
                existing_entry["v_time"] = get_current_time
            else:
                # Добавление нового элемента
                self.fiscals_data[model_kkt].append({
                    "serialNumber": serialNumber,
                    "fn_serial": fn_serial,
                    "v_time": get_current_time
                })
            # Запись обновленного содержимого обратно в service.json
            service.configs.write_json_file(self.fiscals_data, self.fiscals_file)
        except Exception:
            service.logger.logger_service.error(f"Не удалось обновить '{self.fiscals_file}'", exc_info=True)

    def disable_check_fr(self, get_current_time, validation_date, serial_number, i, model_kkt):
        self.get_fiscals_json()
        try:
            difference_in_days = (datetime.strptime(get_current_time, "%Y-%m-%d %H:%M:%S") -
                                  datetime.strptime(validation_date, "%Y-%m-%d %H:%M:%S")).days

            if difference_in_days > self.delete_days:
                service.logger.logger_service.warning(
                    f"С последней валидации ФР №{serial_number} прошло более {self.delete_days} дней, "
                    f"запись будет удалена")

                self.fiscals_data[model_kkt][i] = {
                    "serialNumber": "",
                    "fn_serial": "",
                    "v_time": ""
                }
                service.configs.write_json_file(self.fiscals_data, self.fiscals_file)

                json_name = f"{serial_number}.json"
                json_path = os.path.join(self.date_path, json_name)
                try:
                    if os.path.exists(json_path):
                        os.remove(json_path)
                        service.logger.logger_service.info(f"Файл {json_path} удалён")
                except Exception:
                    service.logger.logger_service.error(f"Не удалось удалить файл '{json_path}'",
                                                        exc_info=True)
                return True
            return False
        except Exception:
            service.logger.logger_service.error(f"Не удалось отключить проверку ФР№{serial_number}", exc_info=True)
            return False

    def remove_empty_serials_from_file(self, model_kkt):
        self.get_fiscals_json()

        try:
            # Проверяем, есть ли пустые serialNumber
            if model_kkt in self.fiscals_data:
                empty_serials_exist = any(not fiscal.get('serialNumber') for fiscal in self.fiscals_data[model_kkt])

                # Только если есть пустые serialNumber, фильтруем и перезаписываем файл
                if empty_serials_exist:
                    self.fiscals_data[model_kkt] = [fiscal for fiscal in self.fiscals_data[model_kkt] if
                                                 fiscal.get('serialNumber')]
                    service.configs.write_json_file(self.fiscals_data, self.fiscals_file)
        except Exception:
            service.logger.logger_service.error(f"Не удалось очистить '{self.fiscals_file}' от неактуальных записей",
                                                exc_info=True)

    def get_uuid(self):
        if ResourceManagement.uuid != None:
            return ResourceManagement.uuid

        try:
            if os.path.exists(self.uuid_file):
                with open(self.uuid_file, 'rb') as f:
                    encrypted_uuid = f.read()
                    stored_uuid = self.decrypt_data(encrypted_uuid)
                    # Проверяем, соответствует ли прочитанное значение формату UUID
                    try:
                        uuid.UUID(stored_uuid)
                        service.logger.logger_service.debug(
                            f"UUID прочитан из файла '{os.path.abspath(self.uuid_file)}': '{stored_uuid}'")
                        ResourceManagement.uuid = stored_uuid
                        return ResourceManagement.uuid
                    except ValueError:
                        service.logger.logger_service.warning(
                            f"Содержимое файла '{os.path.abspath(self.uuid_file)}' не соответствует формату UUID")
        except Exception:
            service.logger.logger_service.error("Не удалось прочитать файл 'uuid'", exc_info=True)

        try:
            # Получение MAC-адреса
            mac_address = self.get_mac_address()

            # Читаем MachineGuid из реестра
            registry_path = r"SOFTWARE\Microsoft\Cryptography"
            key = winreg.OpenKey(
                winreg.HKEY_LOCAL_MACHINE,
                registry_path,
                0,
                winreg.KEY_READ | winreg.KEY_WOW64_64KEY
            )
            machine_guid, _ = winreg.QueryValueEx(key, "MachineGuid")
            service.logger.logger_service.debug(f"Получено значение 'MachineGuid' из реестра: '{machine_guid}'")
            winreg.CloseKey(key)

            # Комбинируем MAC-адрес и MachineGuid
            combined_id = f"{mac_address}:{machine_guid}"
        except Exception:
            service.logger.logger_service.error(f"Не удалось получить MAC-адрес или MachineGuid", exc_info=True)
            combined_id = machine_guid  # Используем только MachineGuid если возникла ошибка

        try:
            # Хэшируем для стабильного UUID
            hash_bytes = hashlib.sha256(combined_id.encode('utf-8')).digest()
            # Создаём UUID из первых 16 байт хэша
            stable_uuid = uuid.UUID(bytes=hash_bytes[:16])
            service.logger.logger_service.debug(f"Сформирован uuid: '{stable_uuid}'")

            encrypted_uuid = self.encrypt_data(str(stable_uuid))

            try:
                with open(self.uuid_file, 'w') as f:
                    f.write(str(encrypted_uuid.decode('utf-8')))
                service.logger.logger_service.debug(
                    f"UUID '{stable_uuid}' записан в файл: '{os.path.abspath(self.uuid_file)}'")
            except Exception:
                service.logger.logger_service.error(
                    f"Не удалось записать UUID в файл '{os.path.abspath(self.uuid_file)}'", exc_info=True)
            ResourceManagement.uuid = str(stable_uuid)
            return ResourceManagement.uuid
        except Exception:
            service.logger.logger_service.error(f"Не удалось сгенерировать uuid", exc_info=True)
            return "Error"

    def get_host_ip(self):
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.connect(("8.8.8.8", 80))
            host_ip = s.getsockname()[0]
            s.close()
            return host_ip
        except Exception:
            service.logger.logger_service.error(f"Не удалось получить IP-адрес хоста", exc_info=True)

    def get_mac_address(self):
        try:
            mac_int = uuid.getnode()
            # uuid.getnode() возвращает MAC как целое число.
            # Если возвращается 0, это может означать, что MAC не был найден,
            # или были какие-то проблемы с получением.
            if mac_int == 0:
                service.logger.logger_service.warning(
                    "uuid.getnode() вернул 0. Возможно, MAC-адрес не найден или есть проблемы "
                    "с получением адреса первой активной сетевой карты."
                )
                return "00:00:00:00:00:00"

            # Преобразуем целое число в шестнадцатеричную строку, дополняя нулями до 12 символов.
            mac_hex = format(mac_int, '012x')

            # Форматируем строку MAC-адреса с двоеточиями
            mac_address = ':'.join(mac_hex[i:i+2] for i in range(0, 12, 2)).upper()

            service.logger.logger_service.debug(f"Получен MAC-адрес: '{mac_address}'")
            return mac_address

        except Exception:
            service.logger.logger_service.error(
                "Не удалось получить MAC-адрес с помощью uuid.getnode()", exc_info=True)
            return "00:00:00:00:00:00"

    def get_hostname(self):
        try:
            hostname = socket.gethostname()
            return hostname
        except Exception:
            hostname = "hostname"
            service.logger.logger_service.error(f"Не удалось получить имя хоста", exc_info=True)
            return hostname

    def get_file_version(self, file_path):
        try:
            info = win32api.GetFileVersionInfo(file_path, '\\')
            version = info['FileVersionMS'] >> 16, info['FileVersionMS'] & 0xFFFF, info['FileVersionLS'] >> 16, info[
                'FileVersionLS'] & 0xFFFF
            service.logger.logger_service.debug(f"Получены метаданные файла '{file_path}':\n{info}")
            return '.'.join(map(str, version))
        except Exception:
            service.logger.logger_service.error(f"Не удалось проверить версию файла: '{file_path}'",
                                              exc_info=True)
            return "Error"

    def rm_old_date(self):
        global rm_date_flag

        if rm_date_flag == 1:
            return

        rm_date_flag = 1
        try:
            if os.path.exists(self.date_path):
                shutil.rmtree(self.date_path)
                service.logger.logger_service.info(f"Старые данные успешно удалены")
        except Exception:
            service.logger.logger_service.error(f"Error: Не удалось удалить старые данные", exc_info=True)

    def current_time(self):
        try:
            timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
            return timestamp
        except Exception:
            service.logger.logger_service.error(f"Не удалось получить текущее время", exc_info=True)

    def save_noip_config(self, noip_url=None, noip_key=None):
        try:
            service.logger.logger_service.debug("Запущен режим настройки NoIP-сервера")

            if noip_url:
                self.config["service"]["noip_connection"]["url"] = \
                    self.encrypt_data(noip_url).decode()

            if noip_key:
                self.config["service"]["noip_connection"]["api_key"] = \
                    self.encrypt_data(noip_key).decode()

            if noip_url or noip_key:
                self.config["service"]["noip_connection"]["encryption"] = True

            service.configs.write_json_file(
                self.config,
                self.config_file
            )

            service.logger.logger_service.info("NoIP-конфигурация успешно сохранена")

        except Exception:
            service.logger.logger_service.updater.error(
                "Не удалось сохранить NoIP-конфигурацию",
                exc_info=True
            )
            os._exit(1)

    def save_telegram_config(self, bot_token=None, chat_id=None):
        try:
            service.logger.logger_service.debug("Запущен режим настройки Telegram бота")

            if bot_token:
                self.config["notification"]["authentication"]["bot_token"] = \
                    self.encrypt_data(bot_token).decode()

            if chat_id:
                self.config["notification"]["authentication"]["chat_id"] = \
                    self.encrypt_data(chat_id).decode()

            if bot_token or chat_id:
                self.config["notification"]["authentication"]["encryption"] = True

            service.configs.write_json_file(
                self.config,
                self.config_file
            )

            service.logger.logger_service.info("Конфигурация Telegram бота успешно сохранена")

        except Exception:
            service.logger.logger_service.error(
                "Не удалось сохранить конфигурацию Telegram бота",
                exc_info=True
            )
            os._exit(1)

    def add_sending_data_endpoint(self, db_url=None, db_apikey=None):
        try:
            service.logger.logger_service.debug(
                "Запущен режим добавления endpoint для отправки данных"
            )

            if not db_url or not db_apikey:
                raise ValueError(
                    f"Usage only:\n"
                    f"posrelaydsc.exe --db-url <db url> --db-apikey <db apikey>"
                )

            entry = {
                "encryption": False,
                "url": db_url,
                "api_key": db_apikey
            }

            if db_apikey:
                entry["api_key"] = self.encrypt_data(db_apikey).decode()
                entry["encryption"] = True

            entry["url"] = self.encrypt_data(db_url).decode()
            entry["encryption"] = True

            self.config["sending_data"]["url_list"].append(entry)

            service.configs.write_json_file(
                self.config,
                self.config_file
            )

            service.logger.logger_service.info(
                "Endpoint успешно добавлен в sending_data.url_list"
            )

        except Exception:
            service.logger.logger_service.error(
                "Не удалось добавить endpoint в sending_data.url_list",
                exc_info=True
            )
            os._exit(1)

class ProcessManagement(ResourceManagement):
    def __init__(self):
        super().__init__()

        try: self.tcp_timeout = int(self.config_connect.get("tcp_timeout", 30))
        except: self.tcp_timeout = 30

    def subprocess_run(self, folder_name, file_name):
        exe_path = os.path.join(about.current_path, folder_name, file_name)
        service.logger.logger_service.info(f"Будет отдана команда на запуск '{exe_path}'")
        try:
            # получаем абсолютный путь к основному файлу скрипта sys.argv[0], а затем с помощью
            # os.path.dirname() извлекаем путь к директории, содержащей основной файл
            working_directory = os.path.join(about.current_path, folder_name)
            subprocess.Popen(exe_path, cwd=working_directory)
        except Exception:
            service.logger.logger_service.error(f"Не удалось запустить '{exe_path}'", exc_info=True)

    def subprocess_kill(self, folder_name, exe_name):
        service.logger.logger_service.debug(f"Будет отдана команда на завершение процесса: '{exe_name}'")
        try:
            command = f"taskkill /F /IM \"{exe_name}\""
            working_directory = os.path.join(about.current_path, folder_name)
            # Выполняем команду в отдельном процессе
            subprocess.Popen(command, shell=True, cwd=working_directory)
        except Exception:
            service.logger.logger_service.error(f"Не удалось отдать команду на завершение процесса: '{exe_name}'",
                                                exc_info=True)
    def check_process(self, file_name):
        try:
            command_str = f'tasklist | findstr /i "{file_name}" >nul'

            result = subprocess.run(
                command_str,
                shell=True,
                capture_output=False,
                creationflags=subprocess.CREATE_NO_WINDOW,
                check=False
            )

            if result.returncode == 0:
                service.logger.logger_service.debug(f"Процесс '{file_name}' активен")
                return True
            elif result.returncode == 1:
                service.logger.logger_service.debug(f"Процесс '{file_name}' неактивен")
                return False
            else:
                # Если returncode не 0 или 1, это указывает на ошибку выполнения команды
                service.logger.logger_service.warning(
                    f"Ошибка выполнения команды CMD для процесса '{file_name}'. Код возврата: {result.returncode}",)
                return None

        except FileNotFoundError:
            # Это исключение может возникнуть, если 'cmd.exe' или одна из команд
            # ('tasklist', 'findstr') не найдена в системном PATH.
            service.logger.logger_service.error( f"Команда CMD или ее компоненты (tasklist/findstr) не найдены. "
                                                 f"Убедитесь, что они доступны в системном PATH.", exc_info=True)
            return None
        except Exception:
            service.logger.logger_service.error(
                f"Не удалось получить статус процесса '{file_name}' через CMD (tasklist|findstr)", exc_info=True)
            return None

    def check_process_cycle(self, exe_name, kill_process=False, count_attempt=6):
        try:
            service.logger.logger_service.debug(f"Проверяем активность процесса '{exe_name}'")
            for attempt in range(count_attempt):
                process_found = self.check_process(exe_name)

                if process_found:
                    if kill_process:
                        self.subprocess_kill("", exe_name)
                    service.logger.logger_service.debug("Следующая проверка через (5) секунд")
                    time.sleep(5)
                    continue
                elif process_found == False:
                    service.logger.logger_service.debug(f"Процесс '{exe_name}' завершил свою работу или не был запущен")
                    return True
                else:
                    service.logger.logger_service.warn("Статус процесса неизвестен, работа службы будет продолжена")
                    return False
            service.logger.logger_service.warn(
                f"Процесс '{exe_name}' остаётся активным в течении (30) секунд, работа службы будет продолжена")
            return False
        except Exception:
            service.logger.logger_service.error(f"Не удалось отследить состояние процесса '{exe_name}'", exc_info=True)

    def check_network(self):
        try:
            # Создаем UDP-сокет
            sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            sock.connect(("8.8.8.8", 80))
            sock.close()
            service.logger.logger_service.debug("Сетевое соединение доступно")
            return True
        except Exception:
            service.logger.logger_service.error("Не удалось проверить доступность сетевого соединения", exc_info=True)
            return False

    def check_network_cycle(self):
        count_attempt = int(self.tcp_timeout / 5 + 1)
        try:
            service.logger.logger_service.debug("Проверяем доступность сетевого соединения")
            for attempt in range(count_attempt):
                network_found = self.check_network()

                if network_found == False:
                    service.logger.logger_service.debug(f"Cледующая проверка через (5) секунд")
                    time.sleep(5)
                    continue
                else:
                    return
            service.logger.logger_service.warn(f"Сетевое соединение остаётся недоступным в течении "
                                               f"({self.tcp_timeout}) секунд. Работа службы будет продолжена")
        except Exception:
            service.logger.logger_service.error("Не удалось проверить доступность сетевого соединения", exc_info=True)

    def network_scanning(self):
        try:
            service.logger.logger_service.info("Начинаем сканирование сети...")
            # Получаем список всех сетевых интерфейсов с их IP-адресами
            interfaces = []
            result = subprocess.run('ipconfig', capture_output=True, text=True, shell=True)

            for line in result.stdout.splitlines():
                if "IPv4" in line and ":" in line:
                    ip = line.split(":")[-1].strip()
                    if ip and not ip.startswith("127."):  # Исключаем локальные адреса
                        prefix = '.'.join(ip.split('.')[:3])
                        interfaces.append(prefix)
                        service.logger.logger_service.debug(f"Найден сетевой интерфейс с IP: {ip}, префикс: {prefix}.xxx")

            if not interfaces:
                # Резервный метод получения хотя бы одного IP-адреса
                host_ip = self.get_host_ip()
                prefix = '.'.join(host_ip.split('.')[:3])
                interfaces.append(prefix)
                service.logger.logger_service.debug(f"Получен IP-адрес хоста: '{host_ip}', префикс: {prefix}.xxx")

            service.logger.logger_service.info(f"Найдено сетевых интерфейсов: {len(interfaces)}")

            # Пингуем все адреса во всех подсетях
            for prefix in interfaces:
                service.logger.logger_service.info(f"Сканирование подсети {prefix}.0/24 для обновления ARP-таблицы...")

                for i in range(1, 255):
                    ip = f"{prefix}.{i}"
                    # Неблокирующий вызов ping с коротким таймаутом
                    subprocess.Popen(
                        f'ping -n 1 -w 100 {ip}',
                        shell=True,
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL
                    )
                # Даем небольшое время для обработки текущей подсети
                time.sleep(1)
            # Дополнительная пауза для завершения всех пингов и обновления ARP-таблицы
            time.sleep(1)
        except Exception:
            service.logger.logger_service.error("Не удалось просканировать локальную сеть", exc_info=True)
