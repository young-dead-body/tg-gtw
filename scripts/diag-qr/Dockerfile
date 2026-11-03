# Запуск diag-qr.mjs в окружении с системными зависимостями Chromium.
# База — официальный Playwright-образ с предустановленными браузерами (/ms-playwright).
FROM mcr.microsoft.com/playwright:v1.49.1-noble

WORKDIR /work

# Копируем только проект и ставим npm-пакет playwright ТОЧНО той версии,
# которая соответствует браузерам в образе (v1.49.1 -> chromium-1148).
COPY package.json diag-qr.mjs ./
RUN npm install --no-audit --no-fund

# Браузеры уже в образе по пути /ms-playwright; не скачиваем их заново.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

ENTRYPOINT ["node", "diag-qr.mjs"]
