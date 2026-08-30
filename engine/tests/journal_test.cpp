#include "limiance/engine/journal.hpp"

#include <gtest/gtest.h>

#include <filesystem>
#include <fstream>
#include <array>
#include <chrono>

using namespace limiance::engine;

namespace {
std::filesystem::path temporary_journal(){return std::filesystem::temp_directory_path()/std::filesystem::path("limiance-journal-"+std::to_string(std::chrono::steady_clock::now().time_since_epoch().count()));}
}

TEST(JournalTest, PersistsAndRecoversOrderedRecords){const auto path=temporary_journal();{Journal journal(path);journal.append(JournalKind::order_command,1,std::array<std::uint8_t,3>{1,2,3});journal.append(JournalKind::trade_event,2,std::array<std::uint8_t,2>{4,5});}{Journal journal(path);const auto records=journal.recover();ASSERT_EQ(records.size(),2);EXPECT_EQ(records[0].sequence,1);EXPECT_EQ(records[1].kind,JournalKind::trade_event);EXPECT_EQ(records[1].payload[1],5);}std::filesystem::remove(path);}

TEST(JournalTest, RejectsChecksumCorruption){const auto path=temporary_journal();{Journal journal(path);journal.append(JournalKind::order_command,1,std::array<std::uint8_t,3>{1,2,3});}{std::fstream file(path,std::ios::binary|std::ios::in|std::ios::out);file.seekp(-1,std::ios::end);file.put(static_cast<char>(9));}Journal journal(path);EXPECT_THROW(journal.recover(),JournalError);std::filesystem::remove(path);}

TEST(JournalTest, RejectsTruncatedRecord){const auto path=temporary_journal();{Journal journal(path);journal.append(JournalKind::order_command,1,std::array<std::uint8_t,3>{1,2,3});}std::filesystem::resize_file(path,std::filesystem::file_size(path)-1);Journal journal(path);EXPECT_THROW(journal.recover(),JournalError);std::filesystem::remove(path);}
