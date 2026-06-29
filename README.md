# POSRelayd

## Описание
Служба предоставляет удалённый доступ к командной строке и автоматический сбор данных с фискальных регистраторов Атол и Mitsu. Поддерживается работа с несколькими ФР.

### Удалённый доступ

Маршрутизация происходит через удалённый сервер. Подключение к удалённым устройствам через отдельное клиентское приложение.

**NoIP-сервер:**
https://github.com/symp1ex/posrelayd-noip

**Клиентское приложение:**
https://github.com/symp1ex/POSRelayv

Для отключения функции необходимо добавить значение `enabled` = `false` в конфиге по пути `.\_resources\remote-access.json` (конфиг создаётся при первом запуске). В нём же хранятся `ID` для подключения и временный пароль.

<details>
<summary>Пример <b>remote-access.json</b></summary>

```json
{
    "enabled": false,
    "id": "-",
    "temp_pass": "-"
}
```
</details>
</br>

Чтобы задать постоянный пароль, нужно запустить исполняемый файл службы с аргументом **`--pass`**

```bash
posrelaydsc.exe --pass 112233
```

### Фискальные регистраторы

Подключение к ФР происходит только при запуске службы. В остальное время служба, с заданным в днях интервалом, сопоставляет по свежим логам номер ФР к номеру ФН и отправляет на сервер подтверждение, если ФН всё ещё актуален. При обнаружении замены ФН планируется перезагрузка в ближайшую ночь, для повторного подключения и получения свежих данных от ФР.

Функционал проверялся только при работе с логами iikoFront. Технически должно работать с любым логом в котором есть записи, содержащие номер ФР и номер ФН в пределах одной строки со специфическими ключами перед ними.

<details>
<summary>Пример получаемого json при подключении к ККТ</summary>

```json
  {
    "modelName": "АТОЛ 22 v2 Ф",
    "serialNumber": "00123456790012",
    "RNM": "0000000001036518",
    "organizationName": "ООО Предприятие",
    "fn_serial": "7380440700067159",
    "datetime_reg": "2024-04-13 07:20:00",
    "dateTime_end": "2099-12-31 00:00:00",
    "ofdName": "ООО Эвотор ОФД",
    "bootVersion": "5.8.100",
    "ffdVersion": "105",
    "INN": "1111222233  ",
    "address": "г.Москва, Тверская ул, д.1, стр. 333",
    "attribute_excise": "True",
    "attribute_marked": "False",
    "fnExecution": "Эмулятор ФН с поддержкой ФФД 1.2",
    "installed_driver": "10.9.1.0",
    "licenses": {
        "1": {
            "name": "Фискальные функции",
            "dateFrom": "2018-11-21 00:00:00",
            "dateUntil": "2038-01-19 03:14:07"
        },
        "10": {
            "name": "ФФД 1.2",
            "dateFrom": "2019-12-26 00:00:00",
            "dateUntil": "2038-01-19 03:14:07"
        }
    },
    "hostname": "Isaac-LT",
    "url_rms": "https://resto.iiko.it:443/resto",
    "teamviewer_id": "111222333",
    "anydesk_id": "222333444",
    "litemanager_id": "ID_1111",
    "current_time": "2024-05-05 21:57:25",
    "v_time": "2024-05-05 21:57:25",
    "uuid": "6cb34107-cfb2-9ba1-b1ff-4823d63ea8d7"
}
```
(bootVersion - начиная с версии 1.1.0.4 не является версией загрузчика, теперь это версия конфигурации.)

</details>

## Установка и настройка

### Требования
- Windows 7/8/10/11 (На `Win7` и `Embedded` может появиться сообщение об ошибке при запуске, тогда понадобится установка обновления безопасности `KB3063858`. Гуглится по номеру обновления и названию Винды, весит 900кб. Для `Win7` отдельный установщик, для `Embedded` отдельный)

- Установленный драйвер АТОЛ версии 10.7.0.0+ 
	(или библиотека драйвера `fptr10.dll` подходящей версии, расположенная в корне с исполняемым файлом службы)

- Python 3.8 32-bit (не требуется, если служба запускается из исполняемого `.exe`-файла)

### Команды управления службой
```bash
# Установка службы
python posrelaydsc.py install

# Запуск службы
python posrelaydsc.py start

# Остановка службы
python posrelaydsc.py stop

# Удаление службы
python posrelaydsc.py remove

# Запуск в debug-режиме
python posrelaydsc.py debug

# Перезапуск
python posrelaydsc.py restart

# Обновление настроек
python posrelaydsc.py [options] update

#Для установки с автозапуском
python posrelaydsc.py --startup auto install
```
<details>
<summary>Опции для запуска только с <b>install</b> или <b>update</b></summary>
  
```bash
Options for 'install' and 'update' commands only:
 --username domain\username : The Username the service is to run under
 --password password : The password for the username
 --startup [manual|auto|disabled|delayed] : How the service starts, default = manual
 --interactive : Allow the service to interact with the desktop.
 --perfmonini file: .ini file to use for registering performance monitor data
 --perfmondll file: .dll file to use when querying the service for
   performance data, default = perfmondata.dll
Options for 'start' and 'stop' commands only:
 --wait seconds: Wait for the service to actually start or stop.
                 If you specify --wait with the 'stop' option, the service
                 and all dependent services will be stopped, each waiting
                 the specified period.
```

</details>
<br>				 
<details>
<summary>То же самое работает если служба собрана в <b>.exe</b>-файл</summary>
  
```bash
# Установка службы
posrelaydsc.exe install

# Запуск службы
posrelaydsc.exe start

# Остановка службы
posrelaydsc.exe stop

# Удаление службы
posrelaydsc.exe remove

# Запуск в debug-режиме
posrelaydsc.exe debug

# Обновление настроек
posrelaydsc.exe [options] update

#Для установки с автозапуском
posrelaydsc.exe --startup auto install 
```

</details>

### Установка службы в систему

Службу нужно установить в автоматическом режиме запуска и добавить триггеры, чтобы она запускалась после групповых политик, т.к. в этот момент необходимые для подключения к ККТ интерфейсы, уже доступны. Так же придётся добавить в исключения защитника Windows каталог с файлами службы.

<details>
<summary>Пример <b>.bat</b>-скрипта для установки службы</b></summary>

```bash
cd /d "%~dp0"

sc stop POSRelayd

posrelaydsc.exe --startup auto install

posrelaydsc.exe start

sc triggerinfo "POSRelayd" start/machinepolicy start/userpolicy

powershell -Command "Add-MpPreference -ExclusionPath '%~dp0'"

pause
```

</details>

## Конфигурация

### service.json
<details>
<summary>Настройки службы</summary>

```json
{
    "service": {
        "noip_connection": {
            "encryption": false,
            "url": "ws://10.127.33.42:22233/ws",
            "api_key": ""
        },
        "updater": {
            "enabled": true,
            "file_name": "updater.exe"
        },
        "log_level": "info",
        "log_days": 7
    },
    "sending_data": {
        "enabled": true,
        "url_list": [
            {
                "encryption": false,
                "url": "https://server.com/api/submit_json",
                "api_key": ""
            }
        ],
        "max_attempts": 5,
        "delay": 2
    },
    "validation_fn": {
        "enabled": true,
        "forced": false,
        "reboot_file": "reboot.bat",
        "interval": 8,
        "trigger_days": 2,
        "target_time": "05:30",
        "delete_days": 25,
        "atol": {
            "logs_dir": "iiko",
            "logs_mask_name": "AtolFiscalRegister",
            "serialNumber_key": "serialNumber=",
            "fnNumber_key": "fnNumber="
        },
        "mitsu": {
            "logs_dir": "iiko",
            "logs_mask_name": "MitsuCRPlugin",
            "serialNumber_key": "SERIAL='",
            "fnNumber_key": "FN='"
        }
    },
    "shtrihscanner": {
        "enabled": true,
        "exe_name": "shtrihscanner.exe"
    },
    "notification": {
        "enabled": false,
        "authentication": {
            "encryption": false,
            "bot_token": "",
            "chat_id": ""
        },
        "max_attempts": 5,
        "delay": 10
    }
}
```

Параметры службы:
- `encryption`: включение шифрования данных для подключения к NoIP-серверу
- `url`: url-адрес NoIP-сервера
- `api_key`: API-ключ для доступа к NoIP-серверу
<br><br>
- `updater_name`: имя исполняемого файла утилиты обновления, расположенного в `updater` (любой .exe или .bat файл)
- `log_level`: уровень логирования
- `log_days`: срок хранения логов (дни)

Параметры передачи данных:
- `enabled`: включение передачи данных API-запросом
- `url_list`: список серверов для передачи, позволяет добавлять неограниченное количество серверов
- `encryption`: включение шифрования данных для подключения к серверу (для каждого сервера в списке включается отдельно)
- `url`: адрес сервера вместе с ручкой
- `api_key`: API-ключ, передаваемый в заголовке запроса
- `max_attempts`: максимум попыток передачи
- `delay`: задержка между попытками

Параметры проверки ФН:
- `enabled`: включение проверки ФН
- `forced`: позволяет пропустить сопоставление ФР к ФН по логам и сразу инициировать запуск `reboot_file` по настроенному ниже расписанию
- `reboot_file`: имя файла скрипта, расположенного в `_resources`, который будет выполнен в указанное время при обнаружении замены ФН (любой .exe или .bat файл)
- `interval`: интервал проверки (часы)
- `trigger_days`: дней до повторного сопоставления ФР к ФН по логам или принудительного запуска `reboot_file` при включенном `forced`
- `target_time`: время выполнения скрипта `reboot_file`, при `false` выполнение скрипта инициируется сразу
- `delete_days`: дней до удаления неактивного ФР
- `logs_dir`: директория с логами (можно использовать абсолютный путь к любой папке с логами)
- `logs_mask_name`: маска имени лог-файла
- `serialNumber_key`: ключ серийного номера ФР в логе
- `fnNumber_key`: ключ номера ФН в логе

Параметры плагина **`shtrihscanner`**:
- `enabled`: запуск исполняемого файла плагина при работе службы
- `exe_name`: имя\путь до запускаемого файла

Параметры уведомлений:
- `enabled`: включение уведомлений
- `encryption`: шифрование данных для подключения боту
- `bot_token`: токен Telegram бота
- `chat_id`: ID чата
- `max_attempts`: максимум попыток передачи
- `delay`: задержка между попытками

<br>Если будете пытаться использовать проверку логов с ПО, отличным от iiko, то имейте в виду что `serialNumber_key` и `fnNumber_key` должны содержать все символы, расположенные непосредственно перед номером в логе, включая кавычки `'` или символы открытия тега `<`.
</details>

### connect.json
<details>
<summary>Настройки подключения к ККТ</summary>

```json
{
    "tcp_timeout": 30,
    "atol": [
        {
            "type_connect": 0,
            "com_port": "COM4",
            "com_baudrate": "115200",
            "ip": "192.168.1.1",
            "ip_port": "5555"
        },
        {
            "type_connect": 0,
            "com_port": "COM10",
            "com_baudrate": "115200",
            "ip": "192.168.1.2",
            "ip_port": "5555"
        }
    ],
    "mitsu": [
        {
            "type_connect": 0,
            "com_port": "COM7",
            "com_baudrate": "115200",
            "ip": "10.127.1.124",
            "ip_port": "8200"
        },
        {
            "type_connect": 0,
            "com_port": "COM20",
            "com_baudrate": "115200",
            "ip": "10.127.1.124",
            "ip_port": "8200"
        }
    ]
}
```

Параметры:
- `tcp_timeout`: время, в течении которого проверяется доступность сети перед продолжением дальнейшей работы, если в типе подключения стоит `2`
- `type_connect`: тип подключения (0 - автопоиск, 1 - COM-порт, 2 - TCP/IP, 3 - USB)
- `com_port`: номер COM-порта
- `com_baudrate`: скорость порта (115200 по умолчанию)
- `ip`: IP-адрес ККТ
- `ip_port`: порт ККТ

<br>**Атол:**<br>
Для ключа **`type_connect[0]`** дефолтным значением является **`0`**. Автопоиск работает только для виртуальных COM-портов. Этот режим лучше всего подходит, когда один или несколько ФР подключены по USB

Значение **`type_connect[1]`** проверяется только если **`type_connect[0]`** не равен **`0`**, т.е. на случай если ФРа два и один из них (или оба) подключен НЕ по USB.

Ключ **`type_connect[1]`** может принимать только два значения:<br>
1 - если второй ФР подключен по COM<br>
2 - если второй ФР подключен по IP<br>
(при любом другом значении ключа `type_connect[1]` или его отсутствии не происходит ничего.)<br>

<br>**Mitsu:**<br>
ККТ Mitsu поддерживают подключение только по COM-портам или TCP\IP. При **`type_connect[0] = 0`** поочередно сканируются все COM-порты, после чего сканируется сеть. 

Первые две найденные ККТ записываются в файл конфигурации, с соответствующим значением **`type_connect`**. Т.е. при изменении на ККТ номера COM-порта\IP-адреса, повторный поиск произведён не будет пока значение для **`type_connect[0]`** не будет вручную изменено на **`0`**.

Если при первом поиске ККТ Mitsu найдены не были, то **`type_connect`** принимает значение **`3`** и повторный поиск так же производиться не будет.
</details>

### Аргументы запуска и шифрование учётных данных в конфиге

Чтобы включить шифрование учётных данных в конфиге и сохранить в него данные для подключения в зашифрованном виде, используются аргументы запуска:

- `--noip-url`: url-адрес NoIP-сервера
- `--noip-key`: API-ключ для доступа к NoIP-серверу
- `--bot-token`: токен Telegram бота
- `--chat-id`: ID чата

(Эти аргументы работают как совместно, так и по отдельности)

Для добавление нового сервера в список серверов на отправку данных, аргументы можно использовать только вместе друг с другом

- `--db-url`: адрес сервера вместе с ручкой
- `--db-apikey`: API-ключ, передаваемый в заголовке запроса

## Отпавка данных API-запросом
Служба, в конце каждого цикла проверки ФН, делает `POST`-запрос на поочередную отправку всех `.json`-файлов, расположенных в папке `date`. `Json` передаются в теле запроса в сыром виде. Ручка запроса задаётся в файле конфигурации вместе с `url`-адресом и `API`-ключом.

### Состав заголовков, передаваемых в запросе:

```python
{
    'Content-Type': 'application/json',
    'X-API-Key': api_key
}
```
<br>Веб-сервер, способный получать и обрабатывать данные от службы тут:
<br>https://github.com/symp1ex/POSRelayd-DB

## updater
Подразумевается, что рядом с исполняемым файлом службы лежит папка **`updater`**, которая содержит приложение\скрипт для загрузки обновления службы и\или передачи полученных от ККТ данных, если не используется встроенное в службу средство передачи данных.

Можно взять тут: 
<br>https://github.com/symp1ex/ftp-updater - до версии 0.6.4.9 включительно, поддерживал передачу всего содержимого папки `date` на FTP-сервер.

Но вообще это может быть любой скрипт, например в виде **`.bat`**-файла. Важно положить его в папку **`updater`** и прописать правильное имя файла в **`service.json`**

В архиве с релизом отсутствует.

## shtrihscanner
При включении будет запущен любой исполняемый файл или скрипт указанный в конфиге. Ожидается что он сохранит полученные от Штриха данные в папку **`date`** в таком же формате, в каком это делает Атол (пример есть в описании).

Специально написанный под это плагин есть тут: 
<br>https://github.com/serty2005/shtrih-kkt

Присутствует в архиве с релизом.

Данные по адресу rms-сервера и удалённым подключениям плагин берёт из **`.json`**, создаваемого службой при запуске. 



## Сборка
### 1. Необходимо сделать замену переменной **`current_path`** в файле **`about.py`**

```python
import os
import sys

version = "2.1.5.5"

'''
в зависимости от того, как запускается служба, нужно менять переменную current_path

#отладочный путь под .py-скрипт
current_path = os.path.dirname(os.path.abspath(__file__))

#путь под собранный .exe-файл
current_path = os.path.dirname(sys.executable)
'''

#отладочный путь под .py-скрипт
current_path = os.path.dirname(os.path.abspath(__file__))
```

Поменять на

```python
import os
import sys

version = "2.1.5.5"

#путь под собранный .exe-файл
current_path = os.path.dirname(sys.executable)
```

### 2. Замена ключа шифрования учётных данных

Переменная **`crypto_key`** в конструкторе класса **`Crypto()`** в файле **`crypto.py`** содержит ключ, которым шифруются учётные данные для подключения к API-сервера и telegram-боту

<details>
<summary><b>crypto.py</b></summary>
  
```python
class Crypto:
    crypto_key = b't_qxC_HN04Tiy1ish2P27ROYSJt_m7_FE2JT6gYngOM='
```
</details>
<br>

В каталоге **`__scripts`** лежит скрипт **`gen-key.py`**, запустив который, можно сгенерировать свой уникальный ключ.

### 3. При сборке при помощи **`PyInstaller`**, необходимо явно указать некоторые импорты. Команда будет выглядеть так:
```bash
py -3.8 -m PyInstaller --hidden-import win32timezone --hidden-import win32serviceutil --hidden-import cryptography.fernet --hidden-import serial.tools.list_ports --hidden-import win32security --hidden-import win32ts --hidden-import win32service --hidden-import win32event  --hidden-import=win32pipe --hidden-import=win32file --hidden-import=win32job --hidden-import=win32security --hidden-import servicemanager --hidden-import socket --hidden-import pywintypes --hidden-import win32api --hidden-import websocket-client --onefile --noconsole --icon=favicon.ico posrelaydsc.py
```

## Отправка уведомлений в Telegram

Нужно создать бота в Telegram и получить его токен. Создать группу, добавить бота в группу как админа и получить **`chat_id`** группы. Указать в **`service.json`** токен и **`chat_id`**

## Примечания
Есть проблема с запуском службы на Windows 7 при старте системы. Временным решением является установка запуска службы в ручном режиме без триггеров запуска и добавление в автозагрузку ярлыка на <b>`.bat`</b>-файл, который запустит исполняемый файл службы с параметром `start`

Пример скрипта:

```bash
@echo off
cd /d "%~dp0"
posrelaydsc.exe start
```