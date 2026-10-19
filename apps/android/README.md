# Heartbeat Checker для Android

Небольшое Android-приложение открывает актуальный кабинет `https://heartbeat-checker.ru/app` в Trusted Web Activity. При отсутствии TWA используется Custom Tab; WebView, JavaScript bridge, локальной копии сайта, собственных cookies и native OAuth нет. Единственный доверенный origin — `https://heartbeat-checker.ru`; внешние страницы и OAuth остаются под управлением браузера. Пароли, сессии и cookies приложение не читает. Пользователь может управлять браузерными данными в самом браузере.

Android 10+ (`minSdk 29`), пакет `ru.heartbeatchecker.app`, `versionName 1.0.0`, `versionCode 1`. App Links ограничены точными `/app`, `/app/settings`, `/app/billing`, `/app/admin`; для `/app` разрешена существующая пара UUID `monitor`+`incident`. Login, подтверждение email, сброс пароля и OAuth callback не перехватываются. Любые другие входящие URL, query и fragment отклоняются. Навигацией внутри TWA и возвратом с внешних страниц управляет браузер.

## Локальные черновики

В другом приложении выберите «Поделиться» → Heartbeat Checker и передайте один полный HTTP(S)-адрес. Native-экран показывает адрес и необязательное название, затем отдельно предлагает сохранить его на устройстве. Сохранение работает без интернета и не делает сетевых запросов к адресу. До 20 черновиков сохраняются в app-private `noBackupFilesDir`, атомарно, без backup/переноса на другое устройство. При повреждении хранилища приложение сохраняет файл и предлагает очистить его вручную.

Сохранённый черновик можно открыть явной кнопкой «Продолжить в кабинете». Он передаётся через `/app#monitor-draft=<base64url UTF-8 JSON>`: `{v:1,id:uuid,url,name?}`. URL ограничен 2048 байтами UTF-8, название — 100 Unicode code points, JSON — 4096 байтами. Не допускаются userinfo, фрагменты, управляющие символы и неизвестные JSON-поля. Передача не создаёт монитор: на сайте нужны подтверждение переноса, проверка формы и ручное сохранение. Черновик остаётся на устройстве до ручного удаления, поскольку браузер не передаёт native-приложению подтверждение создания.

Экран черновиков доступен через ярлык «Черновики» при длительном нажатии на значок приложения, а также после возврата из кабинета. Можно удалить один черновик или очистить все; удаление приложения также удаляет локальные адреса. Авторизация и мониторы на сервере от этого не меняются.

## Сборка

Нужны JDK 17, SDK API 37.0 и Build Tools 36.0.0. Wrapper фиксирует Gradle 9.6.0 с SHA256, AGP — 9.4.0, android-browser-helper — 2.7.3. Нативных библиотек JNI нет.

```sh
sdkmanager 'platforms;android-37.0' 'build-tools;36.0.0'
./gradlew --no-daemon :app:lint :app:testDebugUnitTest :app:assembleDebug
```

SDK задаётся через `ANDROID_HOME` или ignored `local.properties`. Локальные инструменты и временные артефакты можно держать в `.local/`; ключи и APK не добавляются в Git.

Release требует существующий защищённый keystore и четыре переменные:

- `ANDROID_KEYSTORE_FILE` — абсолютный путь к файлу;
- `ANDROID_KEYSTORE_PASSWORD`;
- `ANDROID_KEY_ALIAS`;
- `ANDROID_KEY_PASSWORD`.

Для обоих паролей можно вместо значения передать путь через суффикс `_FILE`. CI получает keystore из защищённого секрета `ANDROID_KEYSTORE_BASE64` и создаёт временный файл. Ключ не генерируется автоматически. Без полной конфигурации упаковка release завершается отказом.

```sh
./gradlew --no-daemon :app:assembleRelease
```

APK: `app/build/outputs/apk/release/app-release.apk`. До публикации обязательны `apksigner verify --verbose --print-certs`, проверка package/version/SDK/permissions и сверка SHA256 сертификата с Digital Asset Links на сайте. Без правильного DAL браузер показывает Custom Tab с адресом; это не подтверждённая TWA. Ключ подписи необходимо сохранить для всех обновлений, а `versionCode` увеличивать при каждом новом APK.

## Приёмка на устройстве

Сборка и unit-тесты не заменяют проверку на Android. Для release проверить install/update, TWA с фактической подписью и Custom Tabs fallback, вход и OAuth в браузере, возврат/Back, точные App Links и отказ callbacks, share/preview/save при отключённой сети, перезапуск процесса и сохранность черновиков, лимит 20 и очистку, TalkBack/масштаб текста, подтверждение переноса и ручное создание монитора в кабинете. Публикация и одобрение RuStore выполняются отдельно; наличие APK само по себе не означает приёмку магазина.

Официальные источники: [AGP 9.4 compatibility](https://developer.android.com/build/releases/agp-9-4-0-release-notes), [SDK repository](https://dl.google.com/android/repository/repository2-3.xml), [android-browser-helper 2.7.3](https://github.com/GoogleChrome/android-browser-helper/releases/tag/android-browser-helper-2.7.3), [TWA](https://developer.chrome.com/docs/android/trusted-web-activity/).
