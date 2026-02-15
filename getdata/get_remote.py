import xml.etree.ElementTree as ET
import os
import winreg
import service.logger
import win32ts
import win32security

class AnyRemoteId:
    user_appdata = None
    server_url = None
    teamviewer_id = None
    anydesk_id = None
    litemanager_id = None

    def get_user_appdata(self, target_folder_path):
        service.logger.logger_service.debug(f"Пытаемся найти домашнюю директорию активного пользователя")
        try:
            # Получаем список активных сессий
            sessions = win32ts.WTSEnumerateSessions(win32ts.WTS_CURRENT_SERVER_HANDLE)

            # Ищем активную консольную сессию
            for session in sessions:
                if session['State'] == win32ts.WTSActive:
                    # Получаем токен пользователя
                    user_token = win32ts.WTSQueryUserToken(session['SessionId'])
                    if user_token:
                        # Получаем информацию о пользователе
                        user_sid = win32security.GetTokenInformation(
                            user_token, win32security.TokenUser)[0]
                        username = win32security.LookupAccountSid(None, user_sid)[0]

                        # Формируем путь
                        user_path = os.path.join('C:\\Users', username, 'AppData', 'Roaming')
                        if os.path.exists(user_path):
                            service.logger.logger_service.debug(
                                f"Найден активный пользователь, его домашняя директория: '{user_path}'")
                            AnyRemoteId.user_appdata = user_path
                            return
            service.logger.logger_service.debug("Активный пользователь не найден")
        except Exception as e:
            service.logger.logger_service.error(f"Ошибка при получении пути AppData: {e}")

            # Если не удалось получить путь активного пользователя,
            # попробуем получить путь первого найденного пользователя у которого будет найден искомый путь
        service.logger.logger_service.debug(
            f"Пытаемся найти пользователя, домашняя директория которого содержит '{target_folder_path}'")
        try:
            users_path = r'C:\Users'
            system_users = ['Public', 'Default', 'Default User', 'All Users',
                            'LocalService', 'NetworkService', 'system']

            # Получаем список всех не системных пользователей
            users = [d for d in os.listdir(users_path)
                     if os.path.isdir(os.path.join(users_path, d))
                     and d not in system_users]

            # Список для хранения пользователей с искомой папкой
            users_with_folder = {}

            for user in users:
                # Составляем полный путь к целевой папке для текущего пользователя
                full_path = os.path.join(users_path, user, 'AppData', 'Roaming', target_folder_path)

                # Проверяем существование папки
                if os.path.exists(full_path):
                    # Получаем время последней модификации
                    mod_time = os.path.getmtime(full_path)
                    users_with_folder[user] = mod_time

            service.logger.logger_service.debug(
                f"Список пользователей, домашняя директория которых содержит искомый путь '{target_folder_path}':\n{users_with_folder}")

            if users_with_folder != {}:
                # Получаем путь к папке первого подходящего пользователя
                latest_user = max(users_with_folder.items(), key=lambda x: x[1])[0]
                home_path = os.path.join(users_path, latest_user, 'AppData', 'Roaming')
                service.logger.logger_service.debug(
                    f"Будет использоваться домашняя директория пользователя: '{latest_user}'")
                AnyRemoteId.user_appdata = home_path
                return
        except Exception as e:
            service.logger.logger_service.error(f"Ошибка при поиске пути пользователя: {e}")


    def get_server_url(self):
        target_folder_path = "iiko\\cashserver"

        if AnyRemoteId.user_appdata == None:
            self.get_user_appdata(target_folder_path)

        xml_path = os.path.join(str(self.user_appdata), 'iiko', 'Cashserver', 'config.xml')
        try:
            # Загрузка XML-файла
            tree = ET.parse(xml_path)
            root = tree.getroot()

            # Находим элемент <serverUrl>
            server_url_element = root.find('serverUrl')

            # Получаем текст из элемента <serverUrl>
            AnyRemoteId.server_url = server_url_element.text
            return AnyRemoteId.server_url
        except FileNotFoundError:
            service.logger.logger_service.warn(f"Файл '{xml_path}' не найден.")
        except Exception:
            service.logger.logger_service.error(f"Произошла ошибка при чтении файла 'cashserver/config.xml'", exc_info=True)

    def get_anydesk_id(self):
        if AnyRemoteId.anydesk_id != None:
            return AnyRemoteId.anydesk_id

        target_folder_path = "anydesk"

        if AnyRemoteId.user_appdata == None:
            self.get_user_appdata(target_folder_path)

        conf_path = os.path.join(str(self.user_appdata), 'anydesk', 'system.conf')
        try:
            with open(conf_path, 'r', encoding='utf-8') as file:
                for line in file:
                    if line.startswith("ad.anynet.id"):
                        ad_anynet_id = line.split("=")[1].strip()
                        AnyRemoteId.anydesk_id = ad_anynet_id
                        return AnyRemoteId.anydesk_id
            service.logger.logger_service.warn(f"Параметр 'ad.anynet.id' не найден в '{conf_path}'.")
            return None
        except FileNotFoundError:
            service.logger.logger_service.warn(f"Файл '{conf_path}' не найден.")
        except Exception:
            service.logger.logger_service.error(f'Error: ошибка при получении anydesk_id из {conf_path}',
                                                exc_info=True)

    def get_teamviewer_id(self):
        if AnyRemoteId.teamviewer_id != None:
            return AnyRemoteId.teamviewer_id

        try:
            # Проверяем раздел реестра для 64-битных приложений
            with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, "SOFTWARE\\TeamViewer", 0,
                                winreg.KEY_READ | winreg.KEY_WOW64_64KEY) as key:
                value, _ = winreg.QueryValueEx(key, "ClientID")
                if value:
                    AnyRemoteId.teamviewer_id = value
                    return AnyRemoteId.teamviewer_id
        except FileNotFoundError:
            pass  # Продолжаем проверку в другом разделе
        except Exception:
            service.logger.logger_service.error(f"Произошла ошибка при чтении реестра", exc_info=True)

        try:
            # Если значение не найдено в разделе для 64-битных приложений,
            # проверяем раздел реестра для 32-битных приложений
            with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, "SOFTWARE\\TeamViewer", 0,
                                winreg.KEY_READ | winreg.KEY_WOW64_32KEY) as key:
                value, _ = winreg.QueryValueEx(key, "ClientID")
                if value:
                    AnyRemoteId.teamviewer_id = value
                    return AnyRemoteId.teamviewer_id
        except FileNotFoundError:
            service.logger.logger_service.warn('Реестровый ключ "ClientID" для TeamViewer не найден.')
        except Exception:
            service.logger.logger_service.error(f"Произошла ошибка при чтении реестра", exc_info=True)

    def get_litemanager_id(self):
        if AnyRemoteId.litemanager_id != None:
            return AnyRemoteId.litemanager_id

        try:
            def search_key_recursively_64(key, subkey): #функция для рекурсивного перебора ключей во вложенных папках
                try:
                    with winreg.OpenKey(key, subkey, 0, winreg.KEY_READ | winreg.KEY_WOW64_64KEY) as current_key:
                        try:
                            value, _ = winreg.QueryValueEx(current_key, "ID (read only)")
                            AnyRemoteId.litemanager_id = value
                            return AnyRemoteId.litemanager_id
                        except FileNotFoundError:
                            pass

                        i = 0
                        while True:
                            try:
                                subkey_name = winreg.EnumKey(current_key, i)
                                result = search_key_recursively_64(current_key, subkey_name)
                                if result:
                                    AnyRemoteId.litemanager_id = result
                                    return AnyRemoteId.litemanager_id
                                i += 1
                            except OSError:
                                break
                except FileNotFoundError:
                    return None

            root_key = winreg.HKEY_LOCAL_MACHINE
            base_subkey = "SOFTWARE\\LiteManager"

            return search_key_recursively_64(root_key, base_subkey)

            id_value = AnyRemoteId.get_litemanager_id()
            if id_value:
                AnyRemoteId.litemanager_id = id_value
                return AnyRemoteId.litemanager_id
            else:
                return None
        except FileNotFoundError:
            service.logger.logger_service.warn('Реестровый ключ "ID (read only)" для LiteManager не найден.')
        except Exception:
            service.logger.logger_service.error(f"Произошла ошибка при чтении реестра", exc_info=True)
