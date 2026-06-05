import service.logger
import service.configs
from cryptography.fernet import Fernet
import base64
import win32crypt
from typing import Literal, Optional, Tuple, Union
import os
from pathlib import Path
from typing import Literal, Tuple
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, ec, rsa, padding

CRYPTPROTECT_LOCAL_MACHINE = 0x4

Algorithm = Literal["ed25519", "ecdsa-p256", "rsa-3072"]

PrivateKey = Union[
    ed25519.Ed25519PrivateKey,
    ec.EllipticCurvePrivateKey,
    rsa.RSAPrivateKey,
]

class Crypto:
    crypto_key = b't_qxC_HN04Tiy1ish2P27ROYSJt_m7_FE2JT6gYngOM='

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

    def encrypt_uuid_dpapi_machine(self, uuid_value: str) -> str:
        encrypted_bytes = win32crypt.CryptProtectData(
            uuid_value.encode("utf-8"),
            "App UUID",
            None,
            None,
            None,
            CRYPTPROTECT_LOCAL_MACHINE
        )

        return base64.b64encode(encrypted_bytes).decode("ascii")

    def decrypt_uuid_dpapi_machine(self, encrypted_uuid: str) -> str:
        encrypted_bytes = base64.b64decode(encrypted_uuid)

        _, decrypted_bytes = win32crypt.CryptUnprotectData(
            encrypted_bytes,
            None,
            None,
            None,
            CRYPTPROTECT_LOCAL_MACHINE
        )

        return decrypted_bytes.decode("utf-8")


class ClientIdentityManager:
    PRIVATE_KEY_PATH = Path(r"C:\ProgramData\POSRelayd\prfpkey.dpapi")
    metadata_path = Path(r"C:\ProgramData\POSRelayd")
    public_key = None

    def __init__(self):
        self.private_key = None
        self.metadata_file = "prfpkey.json"
        self.metadata = service.configs.read_config_file(self.metadata_path, self.metadata_file, "")

    def generate_private_key(self, algorithm: Algorithm = "ed25519",) -> PrivateKey:
        try:
            metadata = os.path.join(self.metadata_path, self.metadata_file)
            if os.path.exists(metadata):
                os.remove(metadata)
        except Exception:
            service.logger.logger_service.error("Не удалось удалить сведения о регистрации", exc_info=True)

        """
        Генерирует приватный ключ выбранного типа.
        """
        try:
            if algorithm == "ed25519":
                return ed25519.Ed25519PrivateKey.generate()

            if algorithm == "ecdsa-p256":
                return ec.generate_private_key(ec.SECP256R1())

            if algorithm == "rsa-3072":
                return rsa.generate_private_key(
                    public_exponent=65537,
                    key_size=3072,
                )

            raise ValueError(f"Unsupported algorithm: {algorithm}")
        except Exception:
            service.logger.logger_service.error("Не удалось сгенерировать приватный ключ", exc_info=True)
    def save_private_key_dpapi(
            self,
            *,
            entropy: Optional[bytes] = None,
    ) -> None:
        try:
            self.PRIVATE_KEY_PATH.parent.mkdir(parents=True, exist_ok=True)

            private_key_der = self.private_key.private_bytes(
                encoding=serialization.Encoding.DER,
                format=serialization.PrivateFormat.PKCS8,
                encryption_algorithm=serialization.NoEncryption(),
            )

            encrypted_private_key = self.dpapi_encrypt(
                private_key_der,
                entropy=entropy,
            )

            self.PRIVATE_KEY_PATH.write_bytes(encrypted_private_key)
        except Exception:
            service.logger.logger_service.error("Не удалось сохранить приватный ключ", exc_info=True)


    def load_private_key_from_dpapi(
            self,
            private_key_path: Union[str, Path],
            *,
            entropy: Optional[bytes] = None,
    ) -> PrivateKey:
        """
        Читает encrypted private key из файла,
        расшифровывает его через DPAPI
        и восстанавливает объект private_key.
        """
        try:
            encrypted_private_key = Path(private_key_path).read_bytes()

            private_key_der = self.dpapi_decrypt(
                encrypted_private_key,
                entropy=entropy,
            )

            private_key = serialization.load_der_private_key(
                private_key_der,
                password=None,
            )

            if not isinstance(
                    private_key,
                    (
                            ed25519.Ed25519PrivateKey,
                            ec.EllipticCurvePrivateKey,
                            rsa.RSAPrivateKey,
                    ),
            ):
                raise TypeError(f"Unsupported private key type: {type(private_key)!r}")

            self.private_key = private_key
            return private_key
        except Exception:
            service.logger.logger_service.error("Не удалось выгрузить приватный ключ из dpapi", exc_info=True)

    def sign_challenge(self,
            challenge: bytes,
    ) -> bytes:
        """
        Подписывает challenge от сервера приватным ключом.

        challenge должен быть bytes.
        Например:
            challenge = bytes.fromhex(server_challenge_hex)
        """
        try:
            if isinstance(self.private_key, ed25519.Ed25519PrivateKey):
                return self.private_key.sign(challenge)

            if isinstance(self.private_key, ec.EllipticCurvePrivateKey):
                return self.private_key.sign(
                    challenge,
                    ec.ECDSA(hashes.SHA256()),
                )

            if isinstance(self.private_key, rsa.RSAPrivateKey):
                return self.private_key.sign(
                    challenge,
                    padding.PSS(
                        mgf=padding.MGF1(hashes.SHA256()),
                        salt_length=padding.PSS.MAX_LENGTH,
                    ),
                    hashes.SHA256(),
                )

            raise TypeError(f"Unsupported private key type: {type(self.private_key)!r}")
        except Exception:
            service.logger.logger_service.error("Не удалось подписать 'challenge' от сервера", exc_info=True)

    def export_public_key_pem(self,
            private_key: PrivateKey):
        """
        Возвращает public key в PEM-формате для отправки на сервер.
        """
        try:
            public_key_pem = private_key.public_key().public_bytes(
                encoding=serialization.Encoding.PEM,
                format=serialization.PublicFormat.SubjectPublicKeyInfo,
            )

            ClientIdentityManager.public_key = public_key_pem.decode("utf-8")
        except Exception:
            service.logger.logger_service.error("Не удалось извлечь публичный ключ", exc_info=True)

    def dpapi_encrypt(
            self,
            data: bytes,
            *,
            entropy: Optional[bytes] = None,
    ) -> bytes:
        try:
            """
            Шифрует bytes через Windows DPAPI.
            По умолчанию привязано к текущему Windows-пользователю.
            """
            return win32crypt.CryptProtectData(
                data,
                None,  # description
                entropy,  # optional entropy
                None,  # reserved
                None,  # prompt struct
                CRYPTPROTECT_LOCAL_MACHINE,  # flags
            )
        except Exception:
            service.logger.logger_service.error(
                "Не удалось выполнить шифрование bytes через Windows DPAPI", exc_info=True)

    def dpapi_decrypt(
            self,
            encrypted_data: bytes,
            *,
            entropy: Optional[bytes] = None,
    ) -> bytes:
        """
        Расшифровывает bytes через Windows DPAPI.
        """
        try:
            _description, data = win32crypt.CryptUnprotectData(
                encrypted_data,
                entropy,  # optional entropy
                None,  # reserved
                None,  # prompt struct
                CRYPTPROTECT_LOCAL_MACHINE,  # flags
            )

            return data
        except Exception:
            service.logger.logger_service.error(
                "Не удалось дешифровать bytes через Windows DPAPI", exc_info=True)

    def init_keypair(
        self,
        algorithm: Algorithm = "ed25519",
        *,
        entropy: Optional[bytes] = None,
    ) -> bool:
        """
        Проверяет наличие приватного ключа.

        Если ключ уже есть:
            - расшифровывает его через DPAPI
            - возвращает private_key и public_key

        Если ключа нет:
            - генерирует новую пару ключей
            - сохраняет private_key через DPAPI
            - возвращает private_key и public_key

        Возвращает:
            private_key: объект приватного ключа
            public_key_pem: публичный ключ в PEM-формате
            created: True, если ключ был создан сейчас; False, если был загружен
        """
        import service.sys_manager
        resources_manager = service.sys_manager.ResourceManagement()
        client_id = str(resources_manager.get_uuid())

        try: register = int(self.metadata.get("register", False))
        except: register = False

        try: reg_uuid = self.metadata.get("uuid", None)
        except: reg_uuid = None

        try:
            if self.PRIVATE_KEY_PATH.exists() and register == True:
                self.private_key = self.load_private_key_from_dpapi(
                    self.PRIVATE_KEY_PATH,
                    entropy=entropy,
                )
                ClientIdentityManager.public_key = None

                if reg_uuid != client_id:
                    self.export_public_key_pem(self.private_key)
                return False

            elif self.PRIVATE_KEY_PATH.exists():
                self.private_key = self.load_private_key_from_dpapi(
                    self.PRIVATE_KEY_PATH,
                    entropy=entropy,
                )
                self.export_public_key_pem(self.private_key)
                return False

            else:
                self.private_key = self.generate_private_key(algorithm)
                self.save_private_key_dpapi(entropy=entropy)
                self.export_public_key_pem(self.private_key)
                return True
        except Exception:
            service.logger.logger_service.error("Не удалось произвести инициализацию ключевой пары", exc_info=True)
