import service.logger
import service.crypto
import service.sys_manager
import service.configs
import json
import base64
from websocket import WebSocketApp


resourcesmanager = service.sys_manager.ResourceManagement()
client_identity = service.crypto.ClientIdentityManager()

encryption_enabled = resourcesmanager.config.get("service", {}).get("noip_connection", {}).get("encryption", False)
SERVER_WS = str(resourcesmanager.config.get("service", {}).get("noip_connection", {}).get("url", ""))
API_KEY = str(resourcesmanager.config.get("service", {}).get("noip_connection", {}).get("api_key", ""))
CLIENT_ID = str(resourcesmanager.get_uuid())


def get_connection_data():
    global SERVER_WS, API_KEY
    try:
        if encryption_enabled == True:
            SERVER_WS = resourcesmanager.decrypt_data(SERVER_WS)
            API_KEY = resourcesmanager.decrypt_data(API_KEY)
            service.logger.logger_service.info("Данные для подключения к NoIP-серверу успешно расшифрованы")
            return
        service.logger.logger_service.warning("Шифрование данных для подключения к NoIP-серверу отключено")
    except Exception:
        service.logger.logger_service.error("Не удалось расшифровать данные для подключения к NoIP-серверу", exc_info=True)

def on_error(ws, error):
    err = str(error)

    # Проверяем, заблокирован ли IP
    if "403" in err or "forbidden" in err.lower():
        service.logger.logger_service.critical(
            "Подключение отклонено сервером: IP заблокирован"
        )

    service.logger.logger_service.error(
        f"WebSocket error: {error}"
    )

def handshake(ws, msg):
    if msg.get("answer") == "register":
        service.configs.create_json_file(
            client_identity.metadata_path, client_identity.metadata_file, {"uuid": CLIENT_ID, "register": 1})
        service.logger.logger_service.info("Запрос на регистрацию успешно подтверждён со стороны сервера")
        return

    if msg.get("answer") == "fail":
        service.logger.logger_service.warning(f"NoIP-сервер отклонил рукопожатие: '{msg.get('description')}'")
        service.logger.logger_service.warning(f"Попытки подключения к NoIP-серверу будут прекращены")
        return

    if msg.get("answer") == "check":
        service.logger.logger_service.debug("NoIP-сервер запросил проверку подписи")
        challenge = msg.get("challenge")

        if not challenge:
            service.logger.logger_service.warning("Не был получен 'challenge' на проверку от NoIP-сервера")
            return

        challenge_bytes = bytes.fromhex(challenge)
        signature = client_identity.sign_challenge(challenge=challenge_bytes)

        if not signature:
            service.logger.logger_service.warning("Не удалось сформировать подпись для 'challenge'")
            return

        signature_b64 = base64.b64encode(signature).decode("ascii")

        try:
            ws.send(json.dumps({
                "type": "sign",
                "id": CLIENT_ID,
                "signature": signature_b64,
            }))
        except Exception:
            service.logger.logger_service.error("Ошибка при отправке 'client_hello'", exc_info=True)
        return

    elif msg.get("answer") == "ok":
        service.logger.logger_service.debug("Сервер подтвердил рукопожатие")
        return

def send_password_once(password: str):
    service.logger.logger_service.info("Сделан запрос на установку постоянного пароля")

    client_identity.init_keypair(algorithm="ed25519")

    try:
        get_connection_data()
        done = False

        def on_open(ws):
            service.logger.logger_service.info("Соединение с NoIP-сервером установлено, WebSocket открыт")
            ws.send(json.dumps({
                "type": "client_hello",
                "id": CLIENT_ID,
                "api_key": API_KEY,
                "password": password,
                "public_key": client_identity.public_key
            }))

        def on_message(ws, message):
            nonlocal done
            msg = json.loads(message)

            if msg.get("type") == "handshake":
                handshake(ws, msg)

            if msg.get("type") == "error":
                service.logger.logger_service.error(
                    f"Ошибка от сервера: {msg.get('error')}"
                )
                ws.close()
                service.logger.logger_service.info("Соединение с NoIP-сервером разорвано, WebSocket закрыт")

            if msg.get("type") == "password_updated":
                done = True
                service.logger.logger_service.info("Сервер подтвердил смену пароля")
                ws.close()
                service.logger.logger_service.info("Соединение с NoIP-сервером разорвано, WebSocket закрыт")

        def on_close(ws, code, reason):
            if not done:
                service.logger.logger_service.warning(
                    f"Соединение закрыто без подтверждения смены пароля (code={code}, reason={reason})"
                )

        ws = WebSocketApp(
            SERVER_WS,
            on_open=on_open,
            on_message=on_message,
            on_close=on_close,
            on_error=on_error
        )

        ws.run_forever()

        if done:
            service.logger.logger_service.info("Пароль успешно изменён")
        else:
            service.logger.logger_service.warning("Пароль не был подтверждён сервером")

    except Exception:
        service.logger.logger_service.error(
            "Не удалось выполнить запрос на изменение постоянного пароля",
            exc_info=True
        )
