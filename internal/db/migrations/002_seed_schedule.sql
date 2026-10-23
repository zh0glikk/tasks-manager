-- Autumn 2026/2027 schedule for E2 Ecology (PhD, year 1)

insert into courses (name, short, teacher, link, color) values
    ('Філософські засади наукового пізнання', 'Філософія', 'проф. Титар О. В.', 'https://meet.google.com/yyi-tqye-jnr', '#6E4F96'),
    ('Іноземна мова для аспірантів (англійська), група 5', 'Англійська', 'доц. Шамаєва Ю. Ю.', 'https://meet.google.com/ywd-gekb-vwx', '#17705F'),
    ('Методологія та організація наукових досліджень', 'Методологія', 'Максименко Н. В.', 'https://meet.google.com/tko-aoys-cns', '#A76620'),
    ('Наукові основи дослідження, моделювання і раціонального використання природних ресурсів', 'Прир. ресурси', 'Максименко Н. В. · Некос А. Н. · Тітенко Г. В.', 'https://meet.google.com/tko-aoys-cns', '#2F5F94');

-- helper: insert rows for one course from a list of dates
create function pg_temp.seed(c_short text, k text, st time, et time, ds text[], tent boolean default false,
                             who text default '', url text default '') returns void as $$
    insert into events (course_id, kind, date, start_time, end_time, tentative, teacher, link)
    select c.id, k, d::date, st, et, tent, who, url
    from courses c, unnest(ds) d
    where c.short = c_short;
$$ language sql;

select pg_temp.seed('Філософія', 'лекція', '15:20', null,
    array['2026-10-01','2026-10-08','2026-10-15','2026-10-22','2026-10-29','2026-11-05','2026-11-12','2026-11-19','2026-11-26','2026-12-03','2026-12-10','2026-12-17']);
select pg_temp.seed('Філософія', 'семінар', '17:00', null,
    array['2026-10-08','2026-10-22','2026-11-05','2026-11-19','2026-12-03','2026-12-17'], true);

select pg_temp.seed('Англійська', 'практичне', '17:00', '18:20',
    array['2026-10-06','2026-10-13','2026-10-20','2026-10-27','2026-11-03','2026-11-10','2026-11-17','2026-11-24','2026-12-01','2026-12-08','2026-12-15','2026-12-22'], true);
select pg_temp.seed('Англійська', 'практичне', '13:40', '15:00',
    array['2026-10-08','2026-10-15','2026-10-22','2026-10-29','2026-11-05','2026-11-12','2026-11-19','2026-11-26','2026-12-03','2026-12-10','2026-12-17','2026-12-24'], true);
select pg_temp.seed('Англійська', 'консультація', '11:30', null, array['2026-11-14']);

select pg_temp.seed('Методологія', 'лекція', '12:00', '13:20',
    array['2026-10-05','2026-10-12','2026-10-19','2026-10-26','2026-11-02','2026-11-09','2026-11-16','2026-11-23','2026-11-30','2026-12-07','2026-12-14']);
select pg_temp.seed('Методологія', 'лекція', '13:40', '15:00', array['2026-12-15']);
select pg_temp.seed('Методологія', 'практичне', '13:40', '15:00',
    array['2026-10-09','2026-10-12','2026-10-19','2026-10-26','2026-11-02','2026-11-09','2026-11-23','2026-11-30','2026-12-07','2026-12-14']);
select pg_temp.seed('Методологія', 'практичне', '12:00', '13:20', array['2026-11-20']);

select pg_temp.seed('Прир. ресурси', 'лекція', '10:10', '11:30', array['2026-10-07'], false, 'Максименко Н. В.', 'https://meet.google.com/tko-aoys-cns');
select pg_temp.seed('Прир. ресурси', 'практичне', '12:00', '13:20', array['2026-10-07'], false, 'Максименко Н. В.', 'https://meet.google.com/tko-aoys-cns');
select pg_temp.seed('Прир. ресурси', 'семінар', '10:10', '11:30', array['2026-10-14'], false, 'Максименко Н. В.', 'https://meet.google.com/tko-aoys-cns');
select pg_temp.seed('Прир. ресурси', 'практичне', '12:00', '13:20', array['2026-10-21'], false, 'Максименко Н. В.', 'https://meet.google.com/tko-aoys-cns');
select pg_temp.seed('Прир. ресурси', 'лекція', '12:00', '13:20', array['2026-10-28'], false, 'Некос А. Н.', 'https://meet.google.com/pet-nvmi-rqy');
select pg_temp.seed('Прир. ресурси', 'практичне', '13:40', '15:00', array['2026-10-28'], false, 'Некос А. Н.', 'https://meet.google.com/pet-nvmi-rqy');
select pg_temp.seed('Прир. ресурси', 'семінар', '12:00', '13:20', array['2026-11-04'], false, 'Некос А. Н.', 'https://meet.google.com/pet-nvmi-rqy');
select pg_temp.seed('Прир. ресурси', 'практичне', '13:40', '15:00', array['2026-11-11'], false, 'Некос А. Н.', 'https://meet.google.com/pet-nvmi-rqy');
select pg_temp.seed('Прир. ресурси', 'лекція', '12:00', '13:20', array['2026-11-18'], false, 'Тітенко Г. В.', 'https://us02web.zoom.us/j/89113335606?pwd=YbCWVHYQf8kOdqg4MPmt6dWPngcKg8.1');
select pg_temp.seed('Прир. ресурси', 'практичне', '12:00', '13:20', array['2026-11-25','2026-12-09'], false, 'Тітенко Г. В.', 'https://us02web.zoom.us/j/89113335606?pwd=YbCWVHYQf8kOdqg4MPmt6dWPngcKg8.1');
select pg_temp.seed('Прир. ресурси', 'семінар', '12:00', '13:20', array['2026-12-02'], false, 'Тітенко Г. В.', 'https://us02web.zoom.us/j/89113335606?pwd=YbCWVHYQf8kOdqg4MPmt6dWPngcKg8.1');

drop function pg_temp.seed;
