import serial
import struct
import socket
import os
import re
import subprocess
import service.logger
import service.configs
import service.sys_manager
import getdata.get_remote
import about

get_remote_id = getdata.get_remote.AnyRemoteId()

class MitsuConnect(service.sys_manager.ProcessManagement):
    get_model = "<GET DEV='?' />"
    get_version = "<GET VER='?' />"
    get_reg_data = "<GET REG='?'/>"
    get_fn_data = "<GET INFO='FN'/>"

    def __init__(self):
        super().__init__()
        self.config_connect = None

        self.fr_0 = None
        self.fr_0_com_port = None
        self.fr_0_com_baudrate = None
        self.fr_0_ip = None
        self.fr_0_ip_port = None

        self.fr_1 = None
        self.fr_1_com_port = None
        self.fr_1_com_baudrate = None
        self.fr_1_ip = None
        self.fr_1_ip_port = None

        self.mitsu_ips = []

    def config_update(self):
        self.config_connect = service.configs.read_config_file(about.current_path, "connect.json",
                                                               service.configs.connect_data, create=True)

        try: self.fr_0 = int(self.config_connect.get("mitsu")[0]["type_connect"])
        except: self.fr_0 = 3

        if self.fr_0 != 3:
            self.fr_0_com_port = self.config_connect.get("mitsu")[0]["com_port"]
            self.fr_0_com_baudrate = self.config_connect.get("mitsu")[0]["com_baudrate"]
            self.fr_0_ip = self.config_connect.get("mitsu")[0]["ip"]
            self.fr_0_ip_port = int(self.config_connect.get("mitsu")[0]["ip_port"])


        try: self.fr_1 = int(self.config_connect.get("mitsu")[1]["type_connect"])
        except: self.fr_1 = 3

        if self.fr_1 != 3:
            self.fr_1_com_port = self.config_connect.get("mitsu")[1]["com_port"]
            self.fr_1_com_baudrate = self.config_connect.get("mitsu")[1]["com_baudrate"]
            self.fr_1_ip = self.config_connect.get("mitsu")[1]["ip"]
            self.fr_1_ip_port = int(self.config_connect.get("mitsu")[1]["ip_port"])

    def calculate_lrc(self, data):
        try:
            # Вычисление контрольной суммы LRC (XOR всех байтов)
            lrc = 0
            for byte in data:
                lrc ^= byte
            return lrc
        except Exception:
            service.logger.kkt.error(f"Не удалось вычислить контрольную сумму LRC", exc_info=True)


    def send_command_to_com(self, command, port, baudrate=115200):
        try:
            # Преобразуем команду в байты (Windows-1251)
            command_bytes = command.encode('cp1251')

            # Формируем пакет
            stx = b'\x02'  # STX (начало пакета)
            length = len(command_bytes)
            length_bytes = struct.pack('<H', length)  # 2 байта, little-endian
            etx = b'\x03'  # ETX (конец пакета)

            # Собираем пакет для расчета LRC
            packet = stx + length_bytes + command_bytes + etx

            # Вычисляем LRC
            lrc = self.calculate_lrc(packet)

            # Добавляем LRC к пакету
            packet += bytes([lrc])

            service.logger.kkt.info(f"Отправлена команда '{command}' на COM-порт '{port}'")
            # Открываем COM-порт
            with serial.Serial(port, baudrate, timeout=0.2, write_timeout=0.2) as ser:
                try:
                    # Отправляем пакет
                    ser.write(packet)
                except serial.SerialTimeoutException:
                    service.logger.kkt.warning("Истекло время ожидания ответа")
                    return None

                # Считываем ответ (без STX и длины)
                response = b''
                while True:
                    byte = ser.read(1)
                    if not byte:
                        break
                    response += byte
                    if len(response) >= 2 and response[-2] == 0x03:  # ETX
                        break

                # Проверяем LRC
                if len(response) >= 2:
                    received_lrc = response[-1]
                    data = response[:-1]
                    calculated_lrc = self.calculate_lrc(data)

                    if received_lrc != calculated_lrc:
                        service.logger.kkt.warning("Неверная контрольная сумма")
                        return None
                    # Возвращаем ответ без ETX и LRC, декодируем из Windows-1251
                    response = data[:-1].decode('cp1251')
                    service.logger.kkt.info("Ответ получен")
                    service.logger.kkt.debug(f"'{response}'")
                    return response
                else:
                    service.logger.kkt.warning("Ответ COM-устройства не подходит под формат")
                    service.logger.kkt.debug(f"Ответ: {response}")
                    return None
        except Exception:
            service.logger.kkt.error(f"Не удалось подключиться к ФР", exc_info=True)
            return None


    def send_command_to_ethernet(self, command, host, port):
        try:
            # Преобразуем команду в байты (Windows-1251)
            command_bytes = command.encode('cp1251')

            # Если команда длиннее 535 байт, разбиваем на пакеты
            packets = []
            if len(command_bytes) > 535:
                for i in range(0, len(command_bytes), 535):
                    chunk = command_bytes[i:i + 535]
                    if i + 535 < len(command_bytes):
                        # Не последний пакет, добавляем ETB (0x17)
                        chunk += b'\x17'
                    packets.append(chunk)
            else:
                packets = [command_bytes]

            service.logger.kkt.info(f"Отправлена команда '{command}' на '{host}:{port}'")
            # Открываем соединение
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.connect((host, port))
                s.settimeout(10)  # Таймаут 10 секунд

                # Отправляем все пакеты
                for packet in packets:
                    s.sendall(packet)

                # Получаем ответ
                response = b''
                while True:
                    chunk = s.recv(1024)
                    if not chunk:
                        break
                    response += chunk

                # Декодируем ответ из Windows-1251
                response_decode = response.decode('cp1251')
                service.logger.kkt.info("Ответ получен")
                service.logger.kkt.debug(f"'{response_decode}'")
                return response_decode

        except Exception:
            service.logger.kkt.error(f"Не удалось подключиться к ФР", exc_info=True)
            return None

    def autodetect_com_port(self, baudrate=115200):
        try:
            import serial.tools.list_ports

            # Получаем список всех доступных COM-портов
            available_ports = [port.device for port in serial.tools.list_ports.comports()]
            service.logger.kkt.info("Начинаем поиск устройств Mitsu на COM-портах")
            service.logger.kkt.info(f"Доступные COM-порты: '{available_ports}'")

            found_ports = []  # Список для хранения найденных портов

            # Проверяем каждый порт
            for port in available_ports:
                try:
                    # Открываем соединение с таймаутом 200 мс
                    with serial.Serial(port, baudrate, timeout=0.2) as ser:
                        # Закрываем и снова открываем для сброса буфера
                        ser.close()
                        ser.open()

                    # Отправляем запрос для получения модели
                    response = self.send_command_to_com(self.get_model, port, baudrate)

                    # Если получили ответ, добавляем порт в список найденных
                    if response is not None:
                        service.logger.kkt.info(f"Устройство Mitsu обнаружено на порту '{port}'")
                        found_ports.append(port)

                        # Если нашли уже 2 порта, возвращаем их
                        if len(found_ports) == 2:
                            service.logger.kkt.info(f"Найдено устройств Mitsu: {len(found_ports)}")
                            service.logger.kkt.debug(f"Порты: '{found_ports}'")
                            return found_ports

                except (serial.SerialException, OSError):
                    # Пропускаем порты, которые не удалось открыть
                    service.logger.kkt.info(f"Не удалось открыть порт '{port}'")
                    continue

            # Если нашли хотя бы одно устройство, возвращаем список
            if found_ports:
                service.logger.kkt.info(f"Найдено устройств Mitsu: {len(found_ports)}")
                service.logger.kkt.debug(f"Порты: '{found_ports}'")
                return found_ports

            service.logger.kkt.warning("Устройств Mitsu не обнаружено ни на одном COM-порту")
            return []

        except Exception:
            service.logger.kkt.error("Ошибка при автоматическом определении COM-порта", exc_info=True)
            return []

    def scanning_arp_table(self):
        try:
            # Сканируем ARP-таблицу для поиска устройств с MAC-адресами, начинающимися с 00-22
            result = subprocess.run('arp -a', capture_output=True, text=True, shell=True)
            service.logger.kkt.debug(f"{result}")
            for line in result.stdout.splitlines():
                if '00-22' in line.upper():  # проверяем MAC-адрес
                    parts = line.split()
                    if len(parts) >= 2:
                        ip = parts[0]
                        self.mitsu_ips.append(ip)

            service.logger.kkt.info(
                f"Найдено устройств Mitsu в локальной сети: {len(self.mitsu_ips)}")
            service.logger.kkt.debug(f"IP-адреса устройств: '{self.mitsu_ips}'")
        except Exception:
            service.logger.kkt.error("Не удалось получить ARP-таблицу", exc_info=True)

    def autodetect_ethernet_device(self, port=8200, timeout=0.2):
        try:
            service.logger.kkt.info("Поиск Mitsu среди известных устройств в локальной сети")
            self.scanning_arp_table()

            if self.mitsu_ips == []:
                service.logger.kkt.info("Поиск Mitsu среди всех устройств в локальной сети")
                self.network_scanning()
                self.scanning_arp_table()

                if self.mitsu_ips == []:
                    service.logger.kkt.info(f"Устройства Mitsu в локальной сети не найдены")
                    return []

            # Проверяем открытость порта 8200 на найденных IP-адресах
            open_port_ips = []

            for ip in self.mitsu_ips:
                try:
                    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                    sock.settimeout(timeout)
                    result = sock.connect_ex((ip, port))
                    sock.close()

                    if result == 0:  # порт открыт
                        open_port_ips.append(ip)
                        service.logger.kkt.debug(f"Порт {port} открыт на {ip}")
                except:
                    service.logger.kkt.debug(f"Ошибка при проверке порта на {ip}")

            service.logger.kkt.debug(f"Найдено устройств с открытым портом {port}: {len(open_port_ips)}")

            # Отправляем команду на устройства и проверяем ответ
            mitsu_devices = []

            for ip in open_port_ips:
                try:
                    response = self.send_command_to_ethernet(self.get_model, ip, port)
                    if response and "<OK DEV=" in response:
                        service.logger.kkt.info(f"Устройство Mitsu обнаружено на {ip}:{port}")
                        mitsu_devices.append(ip)

                        # Если нашли два устройства, останавливаемся
                        if len(mitsu_devices) == 2:
                            break
                except Exception as e:
                    service.logger.kkt.debug(f"Ошибка при опросе {ip}: {str(e)}")

            service.logger.kkt.info(f"Получен ответ от устройств Mitsu: {len(mitsu_devices)}")
            return mitsu_devices

        except Exception:
            service.logger.kkt.error(f"Ошибка при автоматическом определении устройств Mitsu в сети",
                                              exc_info=True)
            return []

    def device_autodetect(self):
        com_port_list = self.autodetect_com_port()

        if len(com_port_list) == 2:
            self.config_connect["mitsu"][0]["type_connect"] = 1
            self.config_connect["mitsu"][0]["com_port"] = com_port_list[0]

            self.config_connect["mitsu"][1]["type_connect"] = 1
            self.config_connect["mitsu"][1]["com_port"] = com_port_list[1]

            service.configs.write_json_file(self.config_connect, "connect.json")
        elif len(com_port_list) == 1:
            self.config_connect["mitsu"][0]["type_connect"] = 1
            self.config_connect["mitsu"][0]["com_port"] = com_port_list[0]

            tcp_port_list = self.autodetect_ethernet_device()
            if tcp_port_list != []:
                self.config_connect["mitsu"][1]["type_connect"] = 2
                self.config_connect["mitsu"][1]["ip"] = tcp_port_list[0]

            service.configs.write_json_file(self.config_connect, "connect.json")
        else:
            tcp_port_list = self.autodetect_ethernet_device()

            if len(tcp_port_list) == 2:
                self.config_connect["mitsu"][0]["type_connect"] = 2
                self.config_connect["mitsu"][0]["ip"] = tcp_port_list[0]

                self.config_connect["mitsu"][1]["type_connect"] = 2
                self.config_connect["mitsu"][1]["ip"] = tcp_port_list[1]

                service.configs.write_json_file(self.config_connect, "connect.json")
            elif len(tcp_port_list) == 1:
                self.config_connect["mitsu"][0]["type_connect"] = 2
                self.config_connect["mitsu"][0]["ip"] = tcp_port_list[0]
                service.configs.write_json_file(self.config_connect, "connect.json")
            else:
                self.config_connect["mitsu"][0]["type_connect"] = 3
                self.config_connect["mitsu"][1]["type_connect"] = 3
                service.configs.write_json_file(self.config_connect, "connect.json")


class MitsuGetData(MitsuConnect):
    def __init__(self):
        super().__init__()
        self.driver_path = "C:\\Program Files\\MITSU.1-F\\MitsuCube.exe"

    def get_driver_version(self):
        driver_version = self.get_file_version(self.driver_path)
        return driver_version

    def get_value_by_tag(self, xml_data, tag):
        try:
            # Очищаем тег от возможных символов
            if tag.startswith('<') and tag.endswith('>'):
                clean_tag = tag[1:-1]
            elif '=' in tag:
                clean_tag = tag.split('=')[0]
            else:
                clean_tag = tag

            # Поиск атрибута
            attr_pattern = rf"{clean_tag}='([^']*)'|{clean_tag}=\"([^\"]*)\""
            attr_match = re.search(attr_pattern, xml_data)
            if attr_match:
                return attr_match.group(1) if attr_match.group(1) is not None else attr_match.group(2)

            # Поиск тега
            tag_pattern = rf"<{clean_tag}>(.*?)</{clean_tag}>"
            tag_match = re.search(tag_pattern, xml_data, re.DOTALL)
            if tag_match:
                return tag_match.group(1)

            return None
        except Exception:
            service.logger.kkt.error(f"Не удалось извлечь данные из полученного ответа от ККТ", exc_info=True)

    def decode_html_entities(self, text):
        replacements = {
            '&quot;': '"',
            '&lt;': '<',
            '&gt;': '>',
            '&amp;': '&',
            '&apos;': "'"
        }

        for entity, char in replacements.items():
            text = text.replace(entity, char)
        return text

    def save_to_fiscals_data(self, model, version, reg_data, fn_data):
        values_ffd_version = {
            1: 100,
            2: 105,
            3: 110,
            4: 120
        }

        try:
            modelDev = self.get_value_by_tag(model, "DEV=")
            modelversion = self.get_value_by_tag(reg_data, "T1188=")
            modelName = f"{modelDev} {modelversion}"

            serialNumber = self.get_value_by_tag(version, "SERIAL=")
            RNM = self.get_value_by_tag(reg_data, "T1037=")

            organizationName_raw = self.get_value_by_tag(reg_data, "<T1048>")
            organizationName = self.decode_html_entities(organizationName_raw)

            fn_serial = self.get_value_by_tag(fn_data, "FN=")
            datetime_reg = self.get_value_by_tag(reg_data, "DATE=")
            dateTime_end = self.get_value_by_tag(fn_data, "VALID=")
            ofdName = self.get_value_by_tag(reg_data, "<T1046>")
            bootVersion = self.get_value_by_tag(version, "VER=")

            ffdVersion_code = int(self.get_value_by_tag(reg_data, "T1209="))
            ffdVersion = values_ffd_version[ffdVersion_code]

            INN = self.get_value_by_tag(reg_data, "T1018=")
            address = self.get_value_by_tag(reg_data, "<T1009>")

            # получаем значение в виде битовой маски
            attributes = int(self.get_value_by_tag(reg_data, "ExtMODE="))
            # проверяем значение 0 и 4 бита
            attribute_excise = bool((attributes >> 0) & 1)
            attribute_marked = bool((attributes >> 4) & 1)

            fnExecution = self.get_value_by_tag(fn_data, "EDITION=")
            hostname = self.get_hostname()
            teamviever_id = get_remote_id.get_teamviewer_id()
            litemanager_id = get_remote_id.get_litemanager_id()
            get_current_time = self.current_time()

            try:
                url_rms = get_remote_id.get_server_url()
            except Exception:
                service.logger.kkt.error(f"Не удалось сохранить адреc rms-сервера", exc_info=True)
                url_rms = "Error"

            try:
                anydesk_id = get_remote_id.get_anydesk_id()
            except Exception:
                service.logger.kkt.error(f"Не удалось сохранить ID anydesk", exc_info=True)
                anydesk_id = "Error"

            date_json = {
                "modelName": modelName,
                "serialNumber": str(serialNumber),
                "RNM": str(RNM),
                "organizationName": str(organizationName),
                "fn_serial": str(fn_serial),
                "datetime_reg": str(datetime_reg),
                "dateTime_end": str(dateTime_end),
                "ofdName": str(ofdName),
                "bootVersion": str(bootVersion),
                "ffdVersion": str(ffdVersion),
                "INN": str(INN),
                "address": str(address),
                "attribute_excise": str(attribute_excise),
                "attribute_marked": str(attribute_marked),
                "fnExecution": str(fnExecution),
                "installed_driver": str(self.get_driver_version()),
                "licenses": "None",
                "hostname": str(hostname),
                "url_rms": str(url_rms),
                "teamviewer_id": str(teamviever_id),
                "anydesk_id": str(anydesk_id),
                "litemanager_id": str(litemanager_id),
                "current_time": str(get_current_time),
                "v_time": str(get_current_time),
                "vc": str(about.version),
                "uuid": self.get_uuid()
            }

            self.rm_old_date()

            folder_name = "date"
            folder_path = os.path.join(about.current_path, folder_name)
            json_name = f"{serialNumber}.json"
            service.configs.create_json_file(folder_path, json_name, date_json)
        except Exception:
            service.logger.kkt.error(f"Не удалось сохранить информацию от ККТ", exc_info=True)

        self.update_correlation_fiscals(serialNumber, fn_serial, get_current_time, "mitsu")


    def get_data_to_com(self, port, baudrate):
        model = self.send_command_to_com(self.get_model, port, baudrate)
        if model == None:
            service.logger.kkt.warning(f"Данные от ККТ не были получены, дальнейшая работа прекращена")
            return

        version = self.send_command_to_com(self.get_version, port, baudrate)
        reg_data = self.send_command_to_com(self.get_reg_data, port, baudrate)
        fn_data = self.send_command_to_com(self.get_fn_data, port, baudrate)
        return model, version, reg_data, fn_data

    def get_data_to_ethernet(self, host, port):
        model = self.send_command_to_ethernet(self.get_model, host, port)
        if model == None:
            service.logger.kkt.warning(f"Данные от ККТ не были получены, дальнейшая работа прекращена")
            return

        version = self.send_command_to_ethernet(self.get_version, host, port)
        reg_data = self.send_command_to_ethernet(self.get_reg_data, host, port)
        fn_data = self.send_command_to_ethernet(self.get_fn_data, host, port)
        return model, version, reg_data, fn_data


    def get_data(self):
        self.config_update()

        if self.fr_0 == 0:
            self.device_autodetect()
            self.config_update()

        if self.fr_0 not in [1, 2] and self.fr_1 not in [1, 2]:
            service.logger.kkt.info(f"В файле конфигурации не задан тип подключения к ККТ Mitsu")
            return

        try: type_connect_atol0 = int(self.config_connect["atol"][0].get("type_connect", 0))
        except: type_connect_atol0 = 0

        try: type_connect_atol1 = int(self.config_connect["atol"][1].get("type_connect", 0))
        except: type_connect_atol1 = 0

        if not (type_connect_atol0 == 2 or type_connect_atol1 == 2):
            self.check_network_cycle()

        try:
            if self.fr_0 == 1:
                model, version, reg_data, fn_data = self.get_data_to_com(self.fr_0_com_port, self.fr_0_com_baudrate)
                self.save_to_fiscals_data(model, version, reg_data, fn_data)
            if self.fr_0 == 2:
                model, version, reg_data, fn_data = self.get_data_to_ethernet(self.fr_0_ip, self.fr_0_ip_port)
                self.save_to_fiscals_data(model, version, reg_data, fn_data)
        except Exception:
            service.logger.kkt.warning(f"Запрос к первой ККТ завершился ошибкой",
                                                exc_info=True)
        if self.fr_1 not in [1, 2]:
            return

        try:
            if self.fr_1 == 1:
                model_1, version_1, reg_data_1, fn_data_1 = self.get_data_to_com(self.fr_1_com_port, self.fr_1_com_baudrate)
                self.save_to_fiscals_data(model_1, version_1, reg_data_1, fn_data_1)
            if self.fr_1 == 2:
                model_1, version_1, reg_data_1, fn_data_1 = self.get_data_to_ethernet(self.fr_1_ip, self.fr_1_ip_port)
                self.save_to_fiscals_data(model_1, version_1, reg_data_1, fn_data_1)
        except Exception:
            service.logger.kkt.warning(f"Запрос ко второй ККТ завершился ошибкой",
                                                exc_info=True)
